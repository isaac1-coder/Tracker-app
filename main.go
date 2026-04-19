package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------- //
// CONSTANTS & CONFIGURATION
// ---------------------------------------------------------------- //

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 512
)

// ---------------------------------------------------------------- //
// DATA MODELS
// ---------------------------------------------------------------- //

type User struct {
	Phone    string          `json:"phone"`
	Nickname string          `json:"nickname"`
	Password string          `json:"password"`
	Lat      float64         `json:"lat"`
	Lng      float64         `json:"lng"`
	Friends  map[string]bool `json:"-"`
	Conn     *websocket.Conn `json:"-"`
	LastSeen time.Time       `json:"last_seen"`
}

type Packet struct {
	Type     string  `json:"type"`
	From     string  `json:"from,omitempty"`
	FromNick string  `json:"fromNick,omitempty"`
	Target   string  `json:"target,omitempty"`
	Text     string  `json:"text,omitempty"`
	Lat      float64 `json:"lat,omitempty"`
	Lng      float64 `json:"lng,omitempty"`
	Phone    string  `json:"phone,omitempty"`
	Nick     string  `json:"nick,omitempty"`
	Pass     string  `json:"pass,omitempty"`
	MsgID    string  `json:"msgId,omitempty"`
	ReplyTo  string  `json:"replyTo,omitempty"`
	RoomID   string  `json:"roomId,omitempty"`
	Accept   bool    `json:"accept,omitempty"`
}

// ---------------------------------------------------------------- //
// GLOBAL REGISTRY
// ---------------------------------------------------------------- //

var (
	users     = make(map[string]*User)
	userMu    sync.RWMutex
	upgrader  = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}
)

// ---------------------------------------------------------------- //
// SERVER CORE
// ---------------------------------------------------------------- //

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}

	// Route Handlers
	http.HandleFunc("/ws", handleWebSocket)
	
	// Static File Server
	fs := http.FileServer(http.Dir("./"))
	http.Handle("/", fs)

	log.Printf("[ZENITH] Heavy-Duty Backend Starting on Port %s", port)
	log.Printf("[SYSTEM] Goroutine Monitoring Active")

	server := &http.Server{
		Addr:         ":" + port,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	// Clean up inactive users every 5 minutes
	go cleanupRoutine()

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("[FATAL] Server Error: %v", err)
	}
}

// ---------------------------------------------------------------- //
// WEBSOCKET LOGIC
// ---------------------------------------------------------------- //

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ERR] Upgrade Fail: %v", err)
		return
	}
	defer conn.Close()

	var sessionUser *User

	// Configuration for this specific connection
	conn.SetReadLimit(maxMessageSize)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error { 
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil 
	})

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if sessionUser != nil {
				log.Printf("[OFFLINE] User %s disconnected", sessionUser.Nickname)
			}
			break
		}

		var p Packet
		if err := json.Unmarshal(msg, &p); err != nil {
			continue
		}

		// Process Signal
		userMu.Lock()
		switch p.Type {
		case "auth":
			sessionUser = handleAuth(p, conn)
		case "location":
			if sessionUser != nil {
				handleLocation(sessionUser, p)
			}
		case "add_friend":
			if sessionUser != nil {
				handleAddFriend(sessionUser, p)
			}
		case "chat", "react", "alert", "call_invite", "call_resp":
			if sessionUser != nil {
				relaySignal(sessionUser, p)
			}
		}
		userMu.Unlock()
	}
}

// ---------------------------------------------------------------- //
// LOGIC HANDLERS
// ---------------------------------------------------------------- //

func handleAuth(p Packet, conn *websocket.Conn) *User {
	u, ok := users[p.Phone]
	if ok {
		if u.Password == p.Pass {
			u.Conn = conn
			u.LastSeen = time.Now()
			conn.WriteJSON(Packet{Type: "auth_ok"})
			return u
		}
		conn.WriteJSON(Packet{Type: "error", Text: "Incorrect password"})
		return nil
	}

	// Signup Logic
	newUser := &User{
		Phone:    p.Phone,
		Nickname: p.Nick,
		Password: p.Pass,
		Friends:  make(map[string]bool),
		Conn:     conn,
		LastSeen: time.Now(),
	}
	users[p.Phone] = newUser
	conn.WriteJSON(Packet{Type: "auth_ok"})
	runtime.GC() // Clear RAM after user creation
	return newUser
}

func handleLocation(u *User, p Packet) {
	u.Lat, u.Lng = p.Lat, p.Lng
	u.LastSeen = time.Now()
	
	// Broadcast to friends only
	for phone := range u.Friends {
		if f, ok := users[phone]; ok && f.Conn != nil {
			f.Conn.WriteJSON(Packet{
				Type: "loc",
				Phone: u.Phone,
				Nick: u.Nickname,
				Lat: u.Lat,
				Lng: u.Lng,
			})
		}
	}
}

func handleAddFriend(u *User, p Packet) {
	target, ok := users[p.Target]
	if !ok {
		u.Conn.WriteJSON(Packet{Type: "note", Text: "User not found in Zenith Database"})
		return
	}

	u.Friends[p.Target] = true
	target.Friends[u.Phone] = true

	// Immediate State Sync
	u.Conn.WriteJSON(Packet{Type: "loc", Phone: target.Phone, Nick: target.Nickname, Lat: target.Lat, Lng: target.Lng})
	target.Conn.WriteJSON(Packet{Type: "loc", Phone: u.Phone, Nick: u.Nickname, Lat: u.Lat, Lng: u.Lng})
	
	u.Conn.WriteJSON(Packet{Type: "note", Text: "Connection established with " + target.Nickname})
}

func relaySignal(u *User, p Packet) {
	target, ok := users[p.Target]
	if !ok || target.Conn == nil {
		u.Conn.WriteJSON(Packet{Type: "note", Text: "User is currently unreachable"})
		return
	}

	// Prepare Relay Packet
	p.From = u.Phone
	p.FromNick = u.Nickname

	// Send to Target
	target.Conn.WriteJSON(p)

	// Acknowledge to Sender for specific types
	if p.Type == "chat" || p.Type == "react" {
		u.Conn.WriteJSON(p)
	}
}

func cleanupRoutine() {
	for {
		time.Sleep(5 * time.Minute)
		userMu.Lock()
		for id, u := range users {
			if u.Conn == nil && time.Since(u.LastSeen) > (24 * time.Hour) {
				delete(users, id)
			}
		}
		userMu.Unlock()
		runtime.GC()
	}
}
