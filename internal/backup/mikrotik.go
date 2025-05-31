package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"tiktocker/internal/util"
	"time"
)

const (
	BackupPath     = "rest/system/backup/save"
	FileGetPath    = "rest/file/read"
	FileInfo       = "rest/file"
	SystemIdentity = "rest/system/identity"

	ContentType = "application/json"
	ChunkSize   = 10 * 1024
)

type BackupSettings struct {
	BaseUrl *url.URL

	EncryptionKey string

	DownloadTo *url.URL

	Timeout time.Duration
}

type BackupFile struct {
	Name     string
	Size     int
	Contents []byte
}

type RequestResult struct {
	File BackupFile

	Err error
}

func MikrotikBackup(settings *BackupSettings, deviceComms chan *RequestResult) {
	util.Log.Infof("backing up Mikrotik: %s", settings.BaseUrl)

	internalChannel := make(chan *RequestResult) //TODO is this mixing channels normal in go?! change types if so
	//TODO if channel is created outside - what about mixing messages in channel?

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	go getIdentity(client, settings, internalChannel)
	systemIdentity := <-internalChannel
	if systemIdentity.Err != nil {
		deviceComms <- &RequestResult{
			Err: fmt.Errorf("failed to get system identity: %v", systemIdentity.Err),
		}
		return
	}

	backupName := systemIdentity.File.Name
	go performBackup(client, backupName, settings, internalChannel)
	backupResponse := <-internalChannel
	if backupResponse.Err != nil {
		deviceComms <- &RequestResult{
			Err: fmt.Errorf("failed to perform backup: %v", backupResponse.Err),
		}
		return
	}

	go fileInfo(client, backupName, settings, internalChannel)
	fileInfoResponse := <-internalChannel
	if fileInfoResponse.Err != nil {
		deviceComms <- &RequestResult{
			Err: fmt.Errorf("failed to get file info: %v", fileInfoResponse.Err),
		}
		return
	}

	go downloadBackup(client, fileInfoResponse.File.Name, fileInfoResponse.File.Size, settings, internalChannel)
	downloadResponse := <-internalChannel
	if downloadResponse.Err != nil {
		deviceComms <- &RequestResult{
			Err: fmt.Errorf("failed to download backup: %v", downloadResponse.Err),
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
		util.Log.Warn("encryption disabled")
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

	resp, err := doRequest(client, &backupRequestUrl, http.MethodPost, &body)
	if err != nil {
		util.Log.Errorf("failed to perform backup: %v", err)
		results <- &RequestResult{Err: err}
		return
	}

	var result map[string]interface{}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&result); err != nil {
		util.Log.Errorf("failed to decode backup response: %v", err)
		results <- &RequestResult{Err: err}
		return
	}
	util.Log.Debugf("backup requested for %s", identity)
	results <- &RequestResult{Err: nil}
}

func fileInfo(client *http.Client, identity string, settings *BackupSettings, results chan<- *RequestResult) {
	fileInfoRequestUrl := *settings.BaseUrl
	q := fileInfoRequestUrl.Query()
	q.Set("name", identity)
	fileInfoRequestUrl.RawQuery = q.Encode()
	fileInfoRequestUrl.Path = fileInfoRequestUrl.ResolveReference(&url.URL{Path: FileInfo}).Path

	util.Log.Infof("requesting backup file info for %s at %s", identity, fileInfoRequestUrl.String())

	resp, err := doRequest(client, &fileInfoRequestUrl, http.MethodGet, nil)
	if err != nil {
		util.Log.Errorf("failed to get file info: %v", err)
		results <- &RequestResult{Err: err}
		return
	}

	var fileInfoList []map[string]interface{}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&fileInfoList); err != nil {
		util.Log.Errorf("failed to decode file info response: %v", err)
		results <- &RequestResult{Err: err}
		return
	}
	if len(fileInfoList) == 0 {
		results <- &RequestResult{Err: fmt.Errorf("backup file not found: %s", identity)}
		return
	}
	size, ok := fileInfoList[0]["size"].(int)
	if !ok {
		results <- &RequestResult{Err: fmt.Errorf("size field not found in file info")}
		return
	}
	results <- &RequestResult{File: BackupFile{Size: size}, Err: nil}
}

func downloadBackup(client *http.Client, fileName string, fileSize int, settings *BackupSettings, results chan<- *RequestResult) {
	fileContent := make([]byte, 0)
	backupFileName := fmt.Sprintf("%s.backup", fileName)
	fileReadPath := *settings.BaseUrl
	fileReadPath.Path = fileReadPath.ResolveReference(&url.URL{Path: FileGetPath}).Path
	const Offset = "offset"

	body := map[string]interface{}{
		"file":       backupFileName,
		"chunk-size": ChunkSize,
		Offset:       0,
	}

	fetchChunk := func(offset int) {
		body[Offset] = offset
		resp, err := doRequest(client, &fileReadPath, "POST", &body)
		if err != nil {
			util.Log.Errorf("Backup request (offset: %d) failed: %v", offset, err)
			results <- &RequestResult{Err: err}
			return
		}
		defer resp.Body.Close()

		fileData, err := io.ReadAll(resp.Body)
		if err != nil {
			util.Log.Errorf("Failed to read backup response: %v", err)
			results <- &RequestResult{Err: err}
			return
		}
		fileContent = append(fileContent, fileData...)
	}

	util.Log.Infof("Downloading backup file %s of size %d bytes, from: %s", fileName, fileSize, fileReadPath.String())

	offset := 0
	done := make(chan bool)
	for offset < fileSize {
		go func(off int) {
			fetchChunk(off)
			done <- true
		}(offset)
		<-done
		offset += ChunkSize
	}

	results <- &RequestResult{File: BackupFile{Contents: fileContent, Size: fileSize, Name: backupFileName}, Err: nil}
}
