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
	BackupPath  = "rest/backup/save"
	FileGetPath = "rest/file/read"
	FileInfo    = "rest/file"

	ContentType = "application/json"
	ChunkSize   = 10 * 1024
)

type BackupSettings struct {
	BackupName    string
	Encrypt       bool
	EncryptionKey string
}

type RequestResult struct {
	fileSize   int
	backupFile []byte

	err error
}

func MikrotikBackup(baseUrl *url.URL, settings BackupSettings, deviceComms chan RequestResult) {
	//TODO if channel is created outside - what about mixing messages in channel?
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	go performBackup(client, baseUrl, settings, deviceComms)

	backupResponse := <-deviceComms

	if backupResponse.err != nil {
		go fileInfo(client, baseUrl, settings, deviceComms)
		fileInfoResponse := <-deviceComms
	} else {
		deviceComms <- RequestResult{
			err: fmt.Errorf("Failed to perform backup: %v", backupResponse.err),
		}
	}
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
		util.Log.Warnf("Request returned status: %s", resp.Status)
		return nil, fmt.Errorf("request returned status: %s", resp.Status)
	}

	return resp, nil
}

// selecting encryption without password has the same effect as selecting no encryption
func performBackup(client *http.Client, baseUrl *url.URL, settings BackupSettings, results chan<- RequestResult) {
	// If encryption is requested but no key is provided, disable encryption
	if settings.Encrypt && settings.EncryptionKey == "" {
		util.Log.Warn("Encryption requested but no encryption key provided; proceeding without encryption")
		settings.Encrypt = false
	}

	body := map[string]interface{}{
		"name":         settings.BackupName,
		"dont-encrypt": !settings.Encrypt,
	}
	if settings.Encrypt {
		body["password"] = settings.EncryptionKey
	}

	backupRequestUrl := *baseUrl
	backupRequestUrl.Path = backupRequestUrl.ResolveReference(&url.URL{Path: BackupPath}).Path

	resp, err := doRequest(client, &backupRequestUrl, http.MethodPost, &body)
	if err != nil {
		util.Log.Errorf("Failed to perform backup: %v", err)
		results <- RequestResult{err: err}
	}

	var result map[string]interface{}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&result); err != nil {
		util.Log.Errorf("Failed to decode backup response: %v", err)
		results <- RequestResult{err: err}
		return
	}
	results <- RequestResult{err: nil}
}

func fileInfo(client *http.Client, baseUrl *url.URL, settings BackupSettings, results chan<- RequestResult) {
	fileInfoRequestUrl := *baseUrl
	q := fileInfoRequestUrl.Query()
	q.Set("name", settings.BackupName)
	fileInfoRequestUrl.RawQuery = q.Encode()
	fileInfoRequestUrl.Path = fileInfoRequestUrl.ResolveReference(&url.URL{Path: FileInfo}).Path

	resp, err := doRequest(client, &fileInfoRequestUrl, http.MethodGet, nil)
	if err != nil {
		util.Log.Errorf("Failed to get file info: %v", err)
		results <- RequestResult{err: err}
	}

	var fileInfoList []map[string]interface{}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&fileInfoList); err != nil {
		util.Log.Errorf("Failed to decode file info response: %v", err)
		results <- RequestResult{err: err}
		return
	}
	if len(fileInfoList) == 0 {
		results <- RequestResult{err: fmt.Errorf("Backup file not found: %s", settings.BackupName)}
		return
	}
	size, ok := fileInfoList[0]["size"].(int)
	if !ok {
		results <- RequestResult{err: fmt.Errorf("size field not found in file info")}
		return
	}
	results <- RequestResult{fileSize: size, err: nil}
}

func downloadBackup(client *http.Client, baseUrl *url.URL, fileSize int, settings BackupSettings, results chan<- RequestResult) {
	fileContent := make([]byte, 0)
	saveUrl := *baseUrl
	saveUrl.Path = saveUrl.ResolveReference(&url.URL{Path: FileGetPath}).Path
	const Offset = "offset"

	body := map[string]interface{}{
		"file":       settings.BackupName + ".backup",
		"chunk-size": ChunkSize,
		Offset:       0,
	}

	fetchChunk := func(offset int) {
		body[Offset] = offset
		resp, err := doRequest(client, &saveUrl, "POST", &body)
		if err != nil {
			util.Log.Errorf("Backup request failed: %v", err)
			results <- RequestResult{err: err}
			return
		}
		defer resp.Body.Close()

		fileData, err := io.ReadAll(resp.Body)
		if err != nil {
			util.Log.Errorf("Failed to read backup response: %v", err)
			results <- RequestResult{err: err}
			return
		}
		fileContent = append(fileContent, fileData...)
	}

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

	results <- RequestResult{backupFile: fileContent, fileSize: fileSize, err: nil}

}
