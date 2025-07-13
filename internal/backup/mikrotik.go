package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/bramvdbogaerde/go-scp"
	"github.com/bramvdbogaerde/go-scp/auth"
	"golang.org/x/crypto/ssh"
	"io"
	"net/http"
	"net/url"

	"tiktocker/internal/common"
)

const (
	BackupPath     = "rest/system/backup/save"
	SystemIdentity = "rest/system/identity"
	ExportPath     = "rest/export"

	ContentType = "application/json"
)

func MikrotikConfigExport(ctx context.Context, settings *common.BackupSettings, httpClient *http.Client, deviceComms chan *common.RequestResult) {
	internalChannel := make(chan *common.RequestResult)
	defer close(internalChannel)

	go getIdentity(httpClient, settings, internalChannel)
	systemIdentityResponse := common.WaitForResult(ctx, internalChannel)
	if systemIdentityResponse.Err != nil {
		deviceComms <- &common.RequestResult{
			Err: fmt.Errorf("backup failure: %v", systemIdentityResponse.Err),
		}
		return
	}
	identity := systemIdentityResponse.MikrotikIdentity

	go exportConfig(httpClient, identity, settings, internalChannel)
	exportConfigResponse := common.WaitForResult(ctx, internalChannel)
	if exportConfigResponse.Err != nil {
		deviceComms <- &common.RequestResult{
			Err: fmt.Errorf("backup failure: %v", exportConfigResponse.Err),
		}
		return
	}
	exportConfigName := exportConfigResponse.File.Name

	go downloadFile(ctx, exportConfigName, settings, internalChannel)
	configDownloadResponse := common.WaitForResult(ctx, internalChannel)
	if configDownloadResponse.Err != nil {
		deviceComms <- &common.RequestResult{
			Err: fmt.Errorf("backup failure: %v", configDownloadResponse.Err),
		}
		return
	}

	deviceComms <- &common.RequestResult{
		MikrotikIdentity: identity,
		File:             configDownloadResponse.File,
	}
}

func MikrotikBackup(ctx context.Context, identity string, settings *common.BackupSettings, httpClient *http.Client, deviceComms chan *common.RequestResult) {
	common.Log.Infof("backing up Mikrotik: %s", settings.BaseUrl.Redacted())

	internalChannel := make(chan *common.RequestResult)
	defer close(internalChannel)

	go performBackup(httpClient, identity, settings, internalChannel)
	backupResponse := common.WaitForResult(ctx, internalChannel)
	if backupResponse.Err != nil {
		deviceComms <- &common.RequestResult{
			Err: fmt.Errorf("backup failure: %v", backupResponse.Err),
		}
		return
	}

	go downloadFile(ctx, backupResponse.File.Name, settings, internalChannel)
	backupDownloadResponse := common.WaitForResult(ctx, internalChannel)
	if backupDownloadResponse.Err != nil {
		deviceComms <- &common.RequestResult{
			Err: fmt.Errorf("backup failure: %v", backupDownloadResponse.Err),
		}
		return
	}

	deviceComms <- backupDownloadResponse
}

func doRequest(client *http.Client, url *url.URL, method string, body *map[string]interface{}) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		common.Log.Errorf("Failed to marshal backup request body: %v", err)
		return nil, err
	}

	req, err := http.NewRequest(method, url.String(), func() io.Reader {
		if method == http.MethodGet {
			return nil
		}
		return bytes.NewBuffer(jsonBody)
	}())
	if err != nil {
		common.Log.Errorf("Failed to create request: %v", err)
		return nil, err
	}
	req.Header.Set("Content-Type", ContentType)

	resp, err := client.Do(req)
	if err != nil {
		common.Log.Errorf("Request failed: %v", err)
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		common.Log.Warnf("request returned status: %s", resp.Status)
		return nil, fmt.Errorf("request returned status: %s", resp.Status)
	}

	return resp, nil
}

func getIdentity(client *http.Client, settings *common.BackupSettings, results chan<- *common.RequestResult) {
	identityUrl := *settings.BaseUrl
	identityUrl.Path = identityUrl.ResolveReference(&url.URL{Path: SystemIdentity}).Path
	common.Log.Debugf("requesting Mikrotik identity %s", identityUrl.Redacted())

	resp, err := doRequest(client, &identityUrl, http.MethodGet, nil)
	if err != nil {
		common.Log.Errorf("failed to get system identity: %v", err)
		results <- &common.RequestResult{Err: err}
		return
	}

	var systemIdentity map[string]string
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&systemIdentity); err != nil {
		common.Log.Errorf("failed to decode system info response: %v", err)
		results <- &common.RequestResult{Err: err}
		return
	}

	common.Log.Debugf("discovered Mikrotik identity: %s", systemIdentity["name"])
	results <- &common.RequestResult{
		MikrotikIdentity: systemIdentity["name"],
		File:             common.BackupFile{Name: systemIdentity["name"]},
		Err:              nil,
	}
}

func exportConfig(client *http.Client, identity string, settings *common.BackupSettings, results chan<- *common.RequestResult) {
	exportUrl := *settings.BaseUrl
	exportUrl.Path = exportUrl.ResolveReference(&url.URL{Path: ExportPath}).Path
	common.Log.Debugf("exporting Mikrotik: %s configuration (this is not a backup)", identity)
	exportFileName := fmt.Sprintf("%s.config.rsc", identity)
	body := map[string]interface{}{
		"file": exportFileName,
	}
	_, err := doRequest(client, &exportUrl, http.MethodPost, &body)
	if err != nil {
		common.Log.Errorf("failed to export config: %v", err)
		results <- &common.RequestResult{Err: err}
		return
	}
	common.Log.Debugf("configuration export requested for %s", identity)
	results <- &common.RequestResult{MikrotikIdentity: identity, File: common.BackupFile{Name: exportFileName}, Err: nil}
}

// selecting encryption without password has the same effect as selecting no encryption
func performBackup(
	client *http.Client,
	identity string,
	settings *common.BackupSettings,
	results chan<- *common.RequestResult,
) {
	encrypt := true
	// If encryption is requested but no key is provided, disable encryption
	if settings.EncryptionKey == "" {
		common.Log.Warnf("encryption disabled for: %s (no encryption key)", identity)
		encrypt = false
	}

	body := map[string]interface{}{
		"name":         identity,
		"dont-encrypt": !encrypt,
	}
	if encrypt {
		body["password"] = settings.EncryptionKey
	}

	backupRequestUrl := *settings.BaseUrl
	backupRequestUrl.Path = backupRequestUrl.ResolveReference(&url.URL{Path: BackupPath}).Path
	common.Log.Debugf("requesting backup for %s at %s", identity, backupRequestUrl.Redacted())

	_, err := doRequest(client, &backupRequestUrl, http.MethodPost, &body) // response is an empty array
	if err != nil {
		common.Log.Errorf("failed to perform backup: %v", err)
		results <- &common.RequestResult{Err: err}
		return
	}

	common.Log.Debugf("backup requested for %s", identity)
	backupFileName := fmt.Sprintf("%s.backup", identity)
	results <- &common.RequestResult{MikrotikIdentity: identity, File: common.BackupFile{Name: backupFileName}, Err: nil}
}

func downloadFile(ctx context.Context, fileName string, settings *common.BackupSettings, results chan<- *common.RequestResult) {
	//scp file, cannot use Mikrotik's REST API for this due to random encoding returned in json

	user := settings.BaseUrl.User.Username()
	pass, _ := settings.BaseUrl.User.Password()
	host := fmt.Sprintf("%s:22", settings.BaseUrl.Host)

	clientConfig, err := auth.PasswordKey(user, pass, ssh.InsecureIgnoreHostKey())
	if err != nil {
		results <- &common.RequestResult{Err: fmt.Errorf("failed to create SSH config: %v", err)}
		return
	}

	client := scp.NewClient(host, &clientConfig)
	err = client.Connect()
	if err != nil {
		results <- &common.RequestResult{Err: fmt.Errorf("failed to SSH to: %s, error: %v", host, err)}
		return
	}
	defer client.Close()

	var buf bytes.Buffer

	err = client.CopyFromRemotePassThru(ctx, &buf, fileName, nil)
	if err != nil {
		results <- &common.RequestResult{Err: fmt.Errorf("failed to SCP file: %v", err)}
		return
	}

	contents := buf.Bytes()
	firstNl := bytes.IndexByte(contents, '\n')
	sha256WithoutFirstLine := ""
	if firstNl >= 0 {
		// Skip date from the first line
		sha256WithoutFirstLine = common.ComputeSha256(contents[firstNl+1:])
	}

	results <- &common.RequestResult{
		File: common.BackupFile{
			Name:                           fileName,
			Contents:                       contents,
			ComputedSha256:                 common.ComputeSha256(contents),
			ComputedSha256WithoutFirstLine: sha256WithoutFirstLine,
		},
		Err: nil,
	}
}
