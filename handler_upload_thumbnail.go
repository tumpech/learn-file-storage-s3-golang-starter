package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

type readAtFile struct {
	io.Reader
	io.Seeker
	io.Closer
}

func (r *readAtFile) ReadAt(p []byte, off int64) (n int, err error) {
	_, err = r.Seek(off, io.SeekStart)
	if err != nil {
		return 0, err
	}
	return r.Read(p)
}

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	// Get the videID from HTTP Path, the check if the video exists
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	// Check if user is authenticated

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// Gather file from request

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	const maxMemory int64 = 10 << 20
	r.ParseMultipartForm(maxMemory)

	file, header, err := r.FormFile("thumbnail")

	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	// Check if it is a PNG or JPEG

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type", err)
		return
	}

	if mediaType != "image/png" && mediaType != "image/jpeg" {
		respondWithError(w, http.StatusBadRequest, "Invalid filetype", nil)
		return
	}

	// Check if the file is the same format as Content-Type

	const mimeDetectionBufferSize = 512
	buffer := make([]byte, mimeDetectionBufferSize)

	// Read the first 512 bytes of the file into the buffer
	_, err = io.ReadFull(file, buffer)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Could not read file for MIME detection", err)
		return
	}

	// Detect the MIME type from the content
	detectedMediaType := http.DetectContentType(buffer)

	// Validate that the detected MIME type matches the Content-Type
	if detectedMediaType != mediaType {
		respondWithError(w, http.StatusBadRequest, "MIME type is different from Content-Type", nil)
		return
	}

	// Rebuild the file stream by combining the buffer and the remaining unread file data
	combinedReader := io.MultiReader(bytes.NewReader(buffer), file)

	file = &readAtFile{
		Reader: combinedReader,
		Seeker: file.(io.Seeker), // Ensures the original file implements io.Seeker
		Closer: file.(io.Closer), // Ensures the original file implements io.Closer
	}

	videoThumbnailExtension, err := mime.ExtensionsByType(mediaType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Content-Type not recognized", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't find video", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "User is not the owner of this video", err)
		return
	}

	randomBytes := make([]byte, 32)
	_, err = rand.Read(randomBytes)

	if err != nil {
		panic("failed to generate random bytes")
	}

	videoThumbnailFilename := base64.RawURLEncoding.EncodeToString(randomBytes)

	videoThumbnailFilenameFull := fmt.Sprintf("%s%s", videoThumbnailFilename, videoThumbnailExtension[0])
	videoThumbnailLocalPath := filepath.Join(cfg.assetsRoot, videoThumbnailFilenameFull)
	videoThumbnailFile, err := os.Create(videoThumbnailLocalPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating new file", err)
		return
	}

	defer videoThumbnailFile.Close()

	_, err = io.Copy(videoThumbnailFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying to new file", err)
		return
	}

	thumbnailURL := fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, videoThumbnailFilenameFull)
	video.ThumbnailURL = &thumbnailURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
