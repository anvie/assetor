package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type WebhookParams struct {
	ChannelID string `json:"channelId"`
	Trim      string `json:"trim"`
	Base      string `json:"base"`
	Url       string `json:"url,omitempty"`
}

type WebhookRequest struct {
	URL           string        `json:"url"`
	ID            string        `json:"id"`
	WebhookParams WebhookParams `json:"webHookParams,omitempty"`
}

type WebhookResponse struct {
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

type ReportPayload struct {
	ID            string        `json:"id"`
	URL           string        `json:"url"`
	WebhookParams WebhookParams `json:"webHookParams,omitempty"`
	Error         string        `json:"error,omitempty"`
}

func downloadVideo(url string, id string) (string, error) {
	const maxRetries = 5
	const timeoutDuration = 10 * time.Minute
	outputTemplate := filepath.Join("downloads",
		fmt.Sprintf("%s_%%(id)s_%%(width)sx%%(height)s_%%(duration>%%H-%%M-%%S)s.%%(ext)s", id))

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		log.Printf("Attempt %d to download video: %s", attempt, url)

		params := []string{}

		if os.Getenv("YTDL_PARAMS") != "" {
			params = strings.Split(os.Getenv("YTDL_PARAMS"), " ")
		}
		// if url not contains x.com then add cookies, (temporary workaround)
		if !strings.Contains(url, "x.com") {
			params = append(params, "--cookies",
				os.Getenv("COOKIES_FILE"))
		} else {
			params = append(params, "--extractor-arg", "twitter:api=legacy")
		}
		params = append(params, "--user-agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36")
		params = append(params, "-o", outputTemplate, url)
		params = append(params, "--max-filesize", "90M")

		cmd := exec.Command("/usr/bin/yt-dlp", params...)

		outputBuffer := &bytes.Buffer{}
		errorBuffer := &bytes.Buffer{}

		cmd.Stdout = outputBuffer
		cmd.Stderr = errorBuffer

		done := make(chan error, 1)
		go func() {
			done <- cmd.Run()
		}()

		select {
		case err := <-done:
			if err != nil {
				log.Printf("Command failed on attempt %d: %s", attempt, err)
				log.Printf("Error buffer output: %s", errorBuffer.String())
				lastErr = fmt.Errorf("command failed: %w", err)
				time.Sleep(1000 * time.Millisecond)
				continue
			}

			if outputBuffer.Len() == 0 {
				log.Printf("Empty output buffer on attempt %d", attempt)
				log.Printf("Error buffer output: %s", errorBuffer.String())
				lastErr = errors.New("output buffer is empty")
				time.Sleep(1000 * time.Millisecond)
				continue
			}

			log.Println("Command output: ", outputBuffer.String())

			re := regexp.MustCompile(`downloads/[\w_]*?_[\w-_]*?_(\d\d-\d\d-\d\d|NA)\.(mp4|webm|mov|mkv|png|jpg|jpeg)`)
			matches := re.FindStringSubmatch(outputBuffer.String())
			if len(matches) > 0 {
				log.Printf("Successfully parsed file name: %s", matches[0])
				return matches[0], nil
			}

			re = regexp.MustCompile(`downloads/.*?_[\w-_]*?_(\d\d-\d\d-\d\d|NA)\.(mp4|webm|mov|mkv|png|jpg|jpeg)`)
			matches = re.FindStringSubmatch(outputBuffer.String())
			if len(matches) > 0 {
				log.Printf("Successfully parsed file name: %s", matches[0])
				return matches[0], nil
			}

			// check if file already downloaded
			re = regexp.MustCompile(`(downloads/.*?_[\w-_]*?_(\d\d-\d\d-\d\d|NA)\.(mp4|webm|mov|mkv|png|jpg|jpeg)).*? has already been downloaded`)
			matches = re.FindStringSubmatch(outputBuffer.String())
			if len(matches) > 1 {
				log.Printf("File already downloaded: %s", matches[1])
				return matches[1], nil
			}

			lastErr = errors.New("failed to parse output file name from yt-dlp output")
		case <-time.After(timeoutDuration):
			_ = cmd.Process.Kill()
			log.Printf("Attempt %d timed out", attempt)
			lastErr = errors.New("download timed out")
		}
	}

	return "", fmt.Errorf("all attempts failed: %w", lastErr)
}

func reportDownloadSuccess(id, file string, webHookParams WebhookParams) {
	reportURL := os.Getenv("REPORT_WEBHOOK_URL")
	if webHookParams.Url != "" {
		reportURL = webHookParams.Url
	}

	// format file to be downloadable url
	//
	fileName := strings.ReplaceAll(file, "downloads/", "download/")
	url := fmt.Sprintf("%s/%s", os.Getenv("DOWNLOAD_BASE_URL"), fileName)

	payload := ReportPayload{
		ID:            id,
		URL:           url,
		WebhookParams: webHookParams,
	}

	log.Printf("Reporting download success for ID: %s, url: %s", id, url)
	log.Printf("Report URL: %s", reportURL)

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Failed to marshal report payload: %s", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, reportURL, bytes.NewBuffer(data))
	if err != nil {
		log.Printf("Failed to create report request: %s", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to send report request: %s", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Report request failed with status: %d", resp.StatusCode)
	}
}

func reportDownloadFailed(id string, webHookParams WebhookParams, err error) {
	reportURL := os.Getenv("REPORT_WEBHOOK_URL")
	if webHookParams.Url != "" {
		reportURL = webHookParams.Url
	}

	payload := ReportPayload{
		ID:            id,
		URL:           "",
		WebhookParams: webHookParams,
		Error:         err.Error(),
	}

	log.Printf("Reporting download failure for ID: %s, error: %s", id, err)

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Failed to marshal report payload: %s", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, reportURL, bytes.NewBuffer(data))
	if err != nil {
		log.Printf("Failed to create report request: %s", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to send report request: %s", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Report request failed with status: %d", resp.StatusCode)
	}
}

func requestToDownloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	var req WebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	if req.URL == "" {
		http.Error(w, "Missing 'url' in payload", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "Missing 'id' in payload", http.StatusBadRequest)
	}

	go func(url, id string, webhookParams WebhookParams) {
		file, err := downloadVideo(url, id)
		if err != nil {
			log.Printf("Failed to download video: %s", err)
			reportDownloadFailed(id, webhookParams, err)
			return
		}

		log.Printf("Downloaded video for ID: %s, file: %s", id, file)

		reportDownloadSuccess(id, file, webhookParams)
	}(req.URL, req.ID, req.WebhookParams)

	resp := WebhookResponse{
		Message: "Download started successfully",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(resp)
}

func downloadFileHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		// Handle preflight requests
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Range")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	fileName := strings.TrimPrefix(r.URL.Path, "/download/")
	if fileName == "" {
		http.Error(w, "Missing file name in path", http.StatusBadRequest)
		return
	}
	if strings.Contains(fileName, "..") {
		http.Error(w, "Invalid file path", http.StatusBadRequest)
		return
	}

	filePath := filepath.Join("downloads", fileName)
	if !strings.HasPrefix(filepath.Clean(filePath), filepath.Clean("downloads")+string(os.PathSeparator)) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	// Set CORS headers to allow access from any origin
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range")

	// w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileName))

	// check file type and set content type
	ext := strings.ToLower(filepath.Ext(fileName))

	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp":
		var imageType string
		switch ext {
		case ".jpg", ".jpeg":
			imageType = "jpeg"
		case ".png":
			imageType = "png"
		case ".gif":
			imageType = "gif"
		case ".bmp":
			imageType = "bmp"
		}
		w.Header().Set("Content-Type", "image/"+imageType)
	default:
		switch ext {
		case ".mp4", ".avi", ".mov", ".flv", ".mkv":
			var videoType string
			switch ext {
			case ".mp4":
				videoType = "mp4"
			case ".avi":
				videoType = "x-msvideo"
			case ".mov":
				videoType = "quicktime"
			case ".flv":
				videoType = "x-flv"
			case ".mkv":
				videoType = "x-matroska"
			}
			w.Header().Set("Content-Type", "video/"+videoType)
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
		}
	}

	http.ServeFile(w, r, filePath)
}

func main() {
	fmt.Println("Assetor v0.0.20")
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	// Ensure downloads directory exists
	if err := os.MkdirAll("downloads", os.ModePerm); err != nil {
		log.Fatalf("Failed to create downloads directory: %s", err)
	}

	http.HandleFunc("/pull", requestToDownloadHandler)
	http.HandleFunc("/download/", downloadFileHandler)

	port := ":4412"
	log.Printf("Starting server on port %s", port)
	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Error starting server: %s", err)
	}
}
