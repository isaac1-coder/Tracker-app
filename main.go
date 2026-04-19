package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------- //
// DATA STRUCTURES
// ---------------------------------------------------------------- //

type User struct {
	Phone    string          `json:"phone"`
	Nickname string          `json:"nickname"`
	Password string          `json:"password"`
	Lat      float64         `json:"lat"`
	Lng      float64         `json:"lng"`
	Friends  map[string]bool `json:"-"`
	Conn     *websocket.Conn `json:"-"`
	Online   bool            `json:"online"`
}

type Packet struct {
	Type     string      `json:"type"`
	From     string      `json:"from,omitempty"`
	FromNick string      `json:"fromNick,omitempty"`
	Target   string      `json:"target,omitempty"`
	Text     string      `json:"text,omitempty"`
	Lat      float64     `json:"lat,omitempty"`
	Lng      float64     `json:"lng,omitempty"`
	Phone    string      `json:"phone,omitempty"`
	Nick     string      `json:"nick,omitempty"`
	MsgID    string      `json:"msgId,omitempty"`
	ReplyTo  string      `json:"replyTo,omitempty"`
	Accept   bool        `json:"accept,omitempty"`
	Msg      string      `json:"msg,omitempty"`
}

// ---------------------------------------------------------------- //
// GLOBAL STATE
// ---------------------------------------------------------------- //

var (
	users       = make(map[string]*User)
	userMutex   sync.RWMutex
	upgrader    = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}
	// Call State
	activeCallLock sync.Mutex
	currentCall    struct {
		Active bool
		Caller string
		Target string
		Start  time.Time
	}
)

// ---------------------------------------------------------------- //
// MAIN SERVER LOGIC
// ---------------------------------------------------------------- //

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}

	// Routes
	http.HandleFunc("/ws", handleWebSocket)
	http.Handle("/", http.FileServer(http.Dir("./")))

	log.Printf("[SERVER] SnapTracker Professional starting on port %s\n", port)
	log.Printf("[MEMORY] Initialized with runtime GC management\n")

	server := &http.Server{
		Addr:         ":" + port,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("[FATAL] Server failed: %s", err)
	}
}

// handleWebSocket manages individual client connections
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ERROR] Upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	var currentUser *User

	// Message Loop
	for {
		mt, message, err := conn.ReadMessage()
		if err != nil {
			if currentUser != nil {
				log.Printf("[OFFLINE] User %s disconnected", currentUser.Nickname)
				currentUser.Online = false
				currentUser.Conn = nil
			}
			break
		}

		// BINARY VOIP RELAY (Priority Path)
		if mt == websocket.BinaryMessage {
			if currentUser != nil {
				relayAudio(currentUser, message)
			}
			continue
		}

		// JSON MESSAGE HANDLING
		var p Packet
		if err := json.Unmarshal(message, &p); err != nil {
			continue
		}

		userMutex.Lock()
		switch p.Type {
		case "auth":
			currentUser = handleAuth(p, conn)
		case "location":
			if currentUser != nil {
				handleLocation(currentUser, p)
			}
		case "add_friend":
			if currentUser != nil {
				handleAddFriend(currentUser, p)
			}
		case "chat":
			if currentUser != nil {
				handleChat(currentUser, p)
			}
		case "react":
			if currentUser != nil {
				handleReaction(currentUser, p)
			}
		case "alert":
			if currentUser != nil {
				handleAlert(currentUser, p)
			}
		case "call_invite":
			if currentUser != nil {
				handleCallInvite(currentUser, p)
			}
		case "call_response":
			if currentUser != nil {
				handleCallResponse(currentUser, p)
			}
		case "transcription":
			if currentUser != nil {
				handleTranscription(currentUser, p)
			}
		}
		userMutex.Unlock()
	}
}

// ---------------------------------------------------------------- //
// HANDLERS
// ---------------------------------------------------------------- //

func handleAuth(p Packet, conn *websocket.Conn) *User {
	u, ok := users[p.Phone]
	if ok {
		if u.Password == p.Text {
			u.Conn = conn
			u.Online = true
			conn.WriteJSON(Packet{Type: "auth_ok"})
			log.Printf("[AUTH] Login success: %s", u.Nickname)
			return u
		}
		conn.WriteJSON(Packet{Type: "error", Msg: "Invalid password"})
		return nil
	}

	// New Signup
	newUser := &User{
		Phone:    p.Phone,
		Nickname: p.Nick,
		Password: p.Text,
		Friends:  make(map[string]bool),
		Conn:     conn,
		Online:   true,
	}
	users[p.Phone] = newUser
	conn.WriteJSON(Packet{Type: "auth_ok"})
	log.Printf("[AUTH] New User: %s", newUser.Nickname)
	runtime.GC() // Efficient RAM clearing
	return newUser
}

func handleLocation(u *User, p Packet) {
	u.Lat = p.Lat
	u.Lng = p.Lng
	// Broadcast to friends
	for phone := range u.Friends {
		if f, ok := users[phone]; ok && f.Conn != nil {
			f.Conn.WriteJSON(Packet{
				Type: "loc",
				Phone: u.Phone,
				Nick:  u.Nickname,
				Lat:   u.Lat,
				Lng:   u.Lng,
			})
		}
	}
}

func handleAddFriend(u *User, p Packet) {
	target, ok := users[p.Target]
	if !ok {
		u.Conn.WriteJSON(Packet{Type: "note", Msg: "Phone number not found"})
		return
	}
	u.Friends[p.Target] = true
	target.Friends[u.Phone] = true

	// Sync both sides immediately
	u.Conn.WriteJSON(Packet{Type: "loc", Phone: target.Phone, Nick: target.Nickname, Lat: target.Lat, Lng: target.Lng})
	target.Conn.WriteJSON(Packet{Type: "loc", Phone: u.Phone, Nick: u.Nickname, Lat: u.Lat, Lng: u.Lng})
	
	u.Conn.WriteJSON(Packet{Type: "note", Msg: "Added " + target.Nickname})
	target.Conn.WriteJSON(Packet{Type: "note", From: u.Nickname, Msg: "User added you!"})
}

func handleChat(u *User, p Packet) {
	target, ok := users[p.Target]
	if ok {
		msg := Packet{
			Type:     "chat",
			From:     u.Phone,
			FromNick: u.Nickname,
			Target:   p.Target,
			Text:     p.Text,
			MsgID:    p.MsgID,
			ReplyTo:  p.ReplyTo,
		}
		target.Conn.WriteJSON(msg)
		u.Conn.WriteJSON(msg)
	}
}

func handleReaction(u *User, p Packet) {
	if target, ok := users[p.Target]; ok {
		target.Conn.WriteJSON(Packet{Type: "react", From: u.Phone, MsgID: p.MsgID})
	}
}

func handleAlert(u *User, p Packet) {
	if target, ok := users[p.Target]; ok {
		target.Conn.WriteJSON(Packet{Type: "ring_alert", From: u.Nickname})
	}
}

func handleCallInvite(u *User, p Packet) {
	activeCallLock.Lock()
	defer activeCallLock.Unlock()

	if currentCall.Active {
		u.Conn.WriteJSON(Packet{Type: "note", Msg: "Lines are busy. Max 1 call allowed."})
		return
	}

	target, ok := users[p.Target]
	if ok {
		currentCall.Active = true
		currentCall.Caller = u.Phone
		currentCall.Target = p.Target
		currentCall.Start = time.Now()

		target.Conn.WriteJSON(Packet{Type: "call_invite", From: u.Phone, FromNick: u.Nickname})
	}
}

func handleCallResponse(u *User, p Packet) {
	caller, ok := users[p.Target]
	if ok {
		if !p.Accept {
			activeCallLock.Lock()
			currentCall.Active = false
			activeCallLock.Unlock()
		}
		caller.Conn.WriteJSON(p)
	}
}

func handleTranscription(u *User, p Packet) {
	if target, ok := users[p.Target]; ok {
		target.Conn.WriteJSON(p)
	}
}

func relayAudio(u *User, data []byte) {
	// Relay to all friends (Simplified VOIP Logic)
	for phone := range u.Friends {
		if f, ok := users[phone]; ok && f.Conn != nil {
			f.Conn.WriteMessage(websocket.BinaryMessage, data)
		}
	}
}

func cleanUpCalls() {
	for {
		time.Sleep(10 * time.Second)
		activeCallLock.Lock()
		if currentCall.Active && time.Since(currentCall.Start) > (3 * time.Minute) {
			currentCall.Active = false
			log.Println("[CLEANUP] 3-Minute Call Limit reached. Ending session.")
			runtime.GC()
		}
		activeCallLock.Unlock()
	}
}

func init() {
	go cleanUpCalls()
}
