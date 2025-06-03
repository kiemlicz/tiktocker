package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	scp "github.com/bramvdbogaerde/go-scp"
	"github.com/bramvdbogaerde/go-scp/auth"
	"golang.org/x/crypto/ssh"
	"io"
	"net/http"
	"net/url"

	"tiktocker/internal/util"
	"time"
)

const (
	BackupPath     = "rest/system/backup/save"
	FileInfo       = "rest/file"
	SystemIdentity = "rest/system/identity"

	ContentType = "application/json"
)

type BackupSettings struct {
	BaseUrl       *url.URL
	EncryptionKey string
	DownloadTo    *url.URL
	Timeout       time.Duration
}

type BackupFile struct {
	Name     string
	Contents []byte
}

type RequestResult struct {
	File BackupFile

	Err error
}

func MikrotikBackup(ctx context.Context, settings *BackupSettings, deviceComms chan *RequestResult) {
	util.Log.Infof("backing up Mikrotik: %s", settings.BaseUrl)

	internalChannel := make(chan *RequestResult) //TODO is this mixing channels normal in go?! change types if so
	//TODO if channel is created outside - what about mixing messages in channel?
	defer close(internalChannel)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	go getIdentity(client, settings, internalChannel)
	systemIdentity := WaitForResult(ctx, internalChannel)
	if systemIdentity.Err != nil {
		deviceComms <- &RequestResult{
			Err: fmt.Errorf("backup failure: %v", systemIdentity.Err),
		}
		return
	}

	backupName := systemIdentity.File.Name
	go performBackup(client, backupName, settings, internalChannel)
	backupResponse := WaitForResult(ctx, internalChannel)
	if backupResponse.Err != nil {
		deviceComms <- &RequestResult{
			Err: fmt.Errorf("backup failure: %v", backupResponse.Err),
		}
		return
	}

	go downloadBackup(ctx, backupResponse.File.Name, settings, internalChannel)
	downloadResponse := WaitForResult(ctx, internalChannel)
	if downloadResponse.Err != nil {
		deviceComms <- &RequestResult{
			Err: fmt.Errorf("backup failure: %v", downloadResponse.Err),
		}
		return
	}

	deviceComms <- downloadResponse
}

func doRequest(client *http.Client, url *url.URL, method string, body *map[string]interface{}) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		util.Log.Errorf("Failed to marshal backup request body: %v", err)
		return nil, err
	}

	req, err := http.NewRequest(method, url.String(), func() io.Reader {
		if method == http.MethodGet {
			return nil
		}
		return bytes.NewBuffer(jsonBody)
	}())
	if err != nil {
		util.Log.Errorf("Failed to create request: %v", err)
		return nil, err
	}
	req.Header.Set("Content-Type", ContentType)

	resp, err := client.Do(req)
	if err != nil {
		util.Log.Errorf("Request failed: %v", err)
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		util.Log.Warnf("request returned status: %s", resp.Status)
		return nil, fmt.Errorf("request returned status: %s", resp.Status)
	}

	return resp, nil
}

func getIdentity(client *http.Client, settings *BackupSettings, results chan<- *RequestResult) {
	identityUrl := *settings.BaseUrl
	identityUrl.Path = identityUrl.ResolveReference(&url.URL{Path: SystemIdentity}).Path
	util.Log.Debugf("requesting Mikrotik identity %s", identityUrl.String())

	resp, err := doRequest(client, &identityUrl, http.MethodGet, nil)
	if err != nil {
		util.Log.Errorf("failed to get system identity: %v", err)
		results <- &RequestResult{Err: err}
		return
	}

	var systemIdentity map[string]string
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&systemIdentity); err != nil {
		util.Log.Errorf("failed to decode system info response: %v", err)
		results <- &RequestResult{Err: err}
		return
	}

	util.Log.Debugf("discovered Mikrotik identity: %s", systemIdentity["name"])
	results <- &RequestResult{File: BackupFile{Name: systemIdentity["name"]}, Err: nil}
}

// selecting encryption without password has the same effect as selecting no encryption
func performBackup(client *http.Client, identity string, settings *BackupSettings, results chan<- *RequestResult) {
	encrypt := true
	// If encryption is requested but no key is provided, disable encryption
	if settings.EncryptionKey == "" {
		util.Log.Warnf("encryption disabled for: %s (no encryption key)", identity)
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
	util.Log.Debugf("requesting backup for %s at %s", identity, backupRequestUrl.String())

	_, err := doRequest(client, &backupRequestUrl, http.MethodPost, &body) // response is an empty array
	if err != nil {
		util.Log.Errorf("failed to perform backup: %v", err)
		results <- &RequestResult{Err: err}
		return
	}

	util.Log.Debugf("backup requested for %s", identity)
	results <- &RequestResult{File: BackupFile{Name: fmt.Sprintf("%s.backup", identity)}, Err: nil}
}

func downloadBackup(ctx context.Context, fileName string, settings *BackupSettings, results chan<- *RequestResult) {
	//scp file, cannot use Mikrotik's REST API for this due to random encoding returned in json

	user := settings.BaseUrl.User.Username()
	pass, _ := settings.BaseUrl.User.Password()
	host := fmt.Sprintf("%s:22", settings.BaseUrl.Host)

	clientConfig, err := auth.PasswordKey(user, pass, ssh.InsecureIgnoreHostKey())
	if err != nil {
		results <- &RequestResult{Err: fmt.Errorf("failed to create SSH config: %v", err)}
		return
	}

	client := scp.NewClient(host, &clientConfig)
	err = client.Connect()
	if err != nil {
		results <- &RequestResult{Err: fmt.Errorf("failed to SSH to: %s, error: %v", host, err)}
		return
	}
	defer client.Close()

	var buf bytes.Buffer

	err = client.CopyFromRemotePassThru(ctx, &buf, fileName, nil)
	if err != nil {
		results <- &RequestResult{Err: fmt.Errorf("failed to SCP file: %v", err)}
		return
	}

	results <- &RequestResult{
		File: BackupFile{
			Name:     fileName,
			Contents: buf.Bytes(),
		},
		Err: nil,
	}
}

// WaitForResult waits for a value from the channel or context cancellation.
func WaitForResult(ctx context.Context, ch <-chan *RequestResult) *RequestResult {
	var zero *RequestResult
	select {
	case v, ok := <-ch:
		if !ok {
			zero.Err = fmt.Errorf("channel closed")
			return zero
		}
		return v
	case <-ctx.Done():
		util.Log.Debugf("context done: %v", ctx.Err())
		zero.Err = fmt.Errorf("context done: %v", ctx.Err())
		return zero
	}
}
