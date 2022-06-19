package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

//go:embed site.html
var siteHtml []byte

func httpBytesHandler(data []byte) http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		_, err := res.Write(data)
		if err != nil {
			return
		}
	})
}

type saveFile struct {
	CurrentMarkerId int      `json:"CurrentMarkerId"`
	Markers         []marker `json:"markers"`
}

type position struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type marker struct {
	Id       int      `json:"id"`
	Author   string   `json:"author"`
	Position position `json:"position"`
}

type logInRequest struct {
	UserName string `json:"userName"`
}

type logInResponse struct {
	SessionId int      `json:"sessionId"`
	Markers   []marker `json:"markers"`
}

type updateRequest struct {
	SessionId int `json:"sessionId"`
}

type updatesResponse struct {
	NewMarkers     []marker            `json:"newMarkers"`
	RemovedMarkers []int               `json:"removedMarkers"`
	UserPositions  map[string]position `json:"userPositions"`
}

type updatePositionRequest struct {
	SessionId int      `json:"sessionId"`
	Position  position `json:"position"`
}

type addMarkerRequest struct {
	SessionId int      `json:"sessionId"`
	Position  position `json:"position"`
}

type removeMarkerRequest struct {
	SessionId int `json:"sessionId"`
	MarkerId  int `json:"markerId"`
}

type session struct {
	id             int
	name           string
	lastPosition   *position // nil wenn keine letzte Position bekannt ist
	lastRequest    time.Time
	newMarkers     []int
	removedMarkers []int
}

func (s *session) isTimedOut() bool {
	const timeoutTime = 10 * time.Second

	return !s.lastRequest.Add(timeoutTime).After(time.Now())
}

type server struct {
	mutex            sync.Mutex
	currentSessionId int
	sessions         map[int]*session
	currentMarkerId  int
	markers          map[int]marker
}

func newServer() *server {
	return &server{
		mutex:            sync.Mutex{},
		currentSessionId: 0,
		sessions:         map[int]*session{},
		currentMarkerId:  0,
		markers:          map[int]marker{},
	}
}

func (s *server) closeTimedOutSessions() {
	for {
		s.mutex.Lock()

		for id, session := range s.sessions {
			if session.isTimedOut() {
				delete(s.sessions, id)
			}
		}

		s.mutex.Unlock()

		time.Sleep(time.Second)
	}
}

func (s *server) loadSaveFile() error {
	file, err := os.Open("./save.json")
	if err != nil {
		if os.IsNotExist(err) {
			file, err = os.Create("./save.json")
			if err != nil {
				return fmt.Errorf("failed to create save file: %w", err)
			}
			defer file.Close()

			s.currentMarkerId = 0
			s.markers = map[int]marker{}

			return nil
		} else {
			return fmt.Errorf("failed to open save file: %w", err)
		}
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read from save file: %w", err)
	}

	var saveFile saveFile
	err = json.Unmarshal(data, &saveFile)
	if err != nil {
		return fmt.Errorf("failed to parse json: %w", err)
	}

	s.currentMarkerId = saveFile.CurrentMarkerId

	markers := make(map[int]marker, len(saveFile.Markers))
	for _, marker := range saveFile.Markers {
		markers[marker.Id] = marker
	}
	s.markers = markers

	return nil
}

func (s *server) saveSaveFile() {
	for {
		time.Sleep(5 * time.Second)

		s.mutex.Lock()

		markers := make([]marker, 0, len(s.markers))
		for _, marker := range s.markers {
			markers = append(markers, marker)
		}

		saveFile := saveFile{
			CurrentMarkerId: s.currentMarkerId,
			Markers:         markers,
		}

		saveData, err := json.Marshal(saveFile)
		if err != nil {
			log.Println(fmt.Errorf("failed to save save file: failed to marshal json: %w", err))
			s.mutex.Unlock()
			continue
		}

		file, err := os.Create("./save.json")
		if err != nil {
			log.Println(fmt.Errorf("faield to save save file: failed to open save file: %w", err))
			s.mutex.Unlock()
			continue
		}

		_, err = file.Write(saveData)
		if err != nil {
			log.Println(fmt.Errorf("faield to save save file: failed to write to save file: %w", err))
			file.Close()
			s.mutex.Unlock()
			continue
		}

		file.Close()

		s.mutex.Unlock()
	}
}

func (s *server) handleLogInRequest(res http.ResponseWriter, req *http.Request) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	defer req.Body.Close()
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return
	}

	var logIn logInRequest
	err = json.Unmarshal(data, &logIn)
	if err != nil {
		return
	}

	s.currentSessionId++

	session := &session{
		id:             s.currentSessionId,
		name:           logIn.UserName,
		lastPosition:   nil,
		lastRequest:    time.Now(),
		newMarkers:     nil,
		removedMarkers: nil,
	}
	s.sessions[session.id] = session

	markers := make([]marker, 0, len(s.markers))
	for _, marker := range s.markers {
		markers = append(markers, marker)
	}

	response := logInResponse{
		SessionId: session.id,
		Markers:   markers,
	}
	responseData, err := json.Marshal(response)
	if err != nil {
		return
	}

	_, err = res.Write(responseData)
	if err != nil {
		return
	}
}

func (s *server) handleUpdateRequest(res http.ResponseWriter, req *http.Request) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	defer req.Body.Close()
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return
	}

	var update updateRequest
	err = json.Unmarshal(data, &update)
	if err != nil {
		return
	}

	currentSession, ok := s.sessions[update.SessionId]
	if !ok {
		return
	}

	currentSession.lastRequest = time.Now()

	userPositions := map[string]position{}
	for _, session := range s.sessions {
		if session.id == currentSession.id {
			continue
		}

		if session.lastPosition == nil {
			continue
		}

		userPositions[session.name] = *session.lastPosition
	}

	newMarkers := make([]marker, len(currentSession.newMarkers))
	for i, markerId := range currentSession.newMarkers {
		newMarkers[i] = s.markers[markerId]
	}

	removedMarkers := currentSession.removedMarkers
	if removedMarkers == nil {
		removedMarkers = []int{}
	}

	response := updatesResponse{
		NewMarkers:     newMarkers,
		RemovedMarkers: removedMarkers,
		UserPositions:  userPositions,
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		return
	}

	_, err = res.Write(responseData)
	if err != nil {
		return
	}

	currentSession.newMarkers = nil
	currentSession.removedMarkers = nil
}

func (s *server) handleUpdatePositionRequest(res http.ResponseWriter, req *http.Request) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	defer req.Body.Close()
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return
	}

	var updatePosition updatePositionRequest
	err = json.Unmarshal(data, &updatePosition)
	if err != nil {
		return
	}

	currentSession, ok := s.sessions[updatePosition.SessionId]
	if !ok {
		return
	}

	currentSession.lastRequest = time.Now()

	currentSession.lastPosition = &updatePosition.Position
}

func (s *server) handleAddMarkerRequest(res http.ResponseWriter, req *http.Request) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	defer req.Body.Close()
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return
	}

	var addMarker addMarkerRequest
	err = json.Unmarshal(data, &addMarker)
	if err != nil {
		return
	}

	currentSession, ok := s.sessions[addMarker.SessionId]
	if !ok {
		return
	}

	currentSession.lastRequest = time.Now()

	s.currentMarkerId++

	m := marker{
		Id:       s.currentMarkerId,
		Author:   currentSession.name,
		Position: addMarker.Position,
	}
	s.markers[m.Id] = m

	for _, session := range s.sessions {
		session.newMarkers = append(session.newMarkers, m.Id)
	}
}

func (s *server) handleRemoveMarkerRequest(res http.ResponseWriter, req *http.Request) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	defer req.Body.Close()
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return
	}

	var removeMarker removeMarkerRequest
	err = json.Unmarshal(data, &removeMarker)
	if err != nil {
		return
	}

	currentSession, ok := s.sessions[removeMarker.SessionId]
	if !ok {
		return
	}

	currentSession.lastRequest = time.Now()

	_, ok = s.markers[removeMarker.MarkerId]
	if !ok {
		return
	}

	delete(s.markers, removeMarker.MarkerId)

	for _, session := range s.sessions {
		removedMarkerAlreadySynced := true

		for i, markerId := range session.newMarkers {
			if markerId == removeMarker.MarkerId {
				removedMarkerAlreadySynced = false
				session.newMarkers = append(session.newMarkers[:i], session.newMarkers[i+1:]...)
				break
			}
		}

		if removedMarkerAlreadySynced {
			session.removedMarkers = append(session.removedMarkers, removeMarker.MarkerId)
		}
	}
}

func (s *server) startServer() error {
	err := s.loadSaveFile()
	if err != nil {
		return fmt.Errorf("failed to load save file: %w", err)
	}

	go s.closeTimedOutSessions()
	go s.saveSaveFile()

	http.HandleFunc("/login", s.handleLogInRequest)
	http.HandleFunc("/update", s.handleUpdateRequest)
	http.HandleFunc("/update_position", s.handleUpdatePositionRequest)
	http.HandleFunc("/add_marker", s.handleAddMarkerRequest)
	http.HandleFunc("/remove_marker", s.handleRemoveMarkerRequest)
	http.Handle("/", httpBytesHandler(siteHtml))

	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Println(fmt.Errorf("failed to start http server: %w", err))
	}

	return nil
}

func main() {
	err := newServer().startServer()
	if err != nil {
		log.Println(fmt.Errorf("failed to start server: %w", err))
	}
}
