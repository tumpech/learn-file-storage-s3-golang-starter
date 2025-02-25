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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
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

	fmt.Println("uploading video", videoID, "by user", userID)

	const maxMemory int64 = 1 << 30
	r.ParseMultipartForm(maxMemory)

	file, header, err := r.FormFile("video")

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

	if mediaType != "video/mp4" {
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

	videoExtension, err := mime.ExtensionsByType(mediaType)
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
		respondWithError(w, http.StatusUnauthorized, "User is not the owner of this video", nil)
		return
	}

	videoLocalFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating temp file", err)
		return
	}

	defer os.Remove(videoLocalFile.Name())
	defer videoLocalFile.Close()

	_, err = io.Copy(videoLocalFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying to temp file", err)
		return
	}

	var folderName string
	aspectRatio, err := getVideoAspectRatio(videoLocalFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error checking the aspect ratio of videofile", err)
		return
	}

	switch aspectRatio {
	case "1.78":
		folderName = "landscape"
	case "0.56":
		folderName = "portrait"
	default:
		folderName = "other"
	}

	videoLocalOutputFileName, err := processVideoForFastStart(videoLocalFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error processing file", err)
		return
	}

	videoLocalOutputFile, err := os.OpenFile(videoLocalOutputFileName, os.O_RDONLY, 0644)

	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error reading processed file", err)
		return
	}

	defer os.Remove(videoLocalOutputFile.Name())
	defer videoLocalOutputFile.Close()

	randomBytes := make([]byte, 32)
	_, err = rand.Read(randomBytes)

	if err != nil {
		panic("failed to generate random bytes")
	}

	videoFilename := base64.RawURLEncoding.EncodeToString(randomBytes)
	s3Key := fmt.Sprintf("%s/%s%s", folderName, videoFilename, videoExtension[0])

	s3PutObjectInput := s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(s3Key),
		Body:        videoLocalOutputFile,
		ContentType: &mediaType,
	}

	_, err = cfg.s3Client.PutObject(r.Context(), &s3PutObjectInput)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't upload video", err)
		return
	}

	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, s3Key)
	video.VideoURL = &videoURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
