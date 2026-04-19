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

// --- Constants ---
const (
	pingPeriod = 20 * time.Second
	pongWait   = 60 * time.Second
)

// --- Models ---
type User struct {
	Phone    string          `json:"phone"`
	Nickname string          `json:"nickname"`
	Password string          `json:"password"`
	Lat      float64         `json:"lat"`
	Lng      float64         `json:"lng"`
	Friends  map[string]bool `json:"-"`
	Conn     *websocket.Conn `json:"-"`
	InCall   bool            `json:"in_call"`
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
	RoomID   string  `json:"roomId,omitempty"`
	MsgID    string  `json:"msgId,omitempty"`
}

// --- Registry ---
var (
	users   = make(map[string]*User)
	userMu  sync.RWMutex
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

func main() {
	port := os.Getenv("PORT")
	if port == "" { port = "10000" }

	http.HandleFunc("/ws", handleWS)
	http.Handle("/", http.FileServer(http.Dir("./")))

	log.Printf("[TITAN] Apex Engine Online on :%s", port)
	
	// Background GC to keep Render RAM tiny
	go func() {
		for {
			time.Sleep(2 * time.Minute)
			runtime.GC()
		}
	}()

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil { return }
	defer conn.Close()

	var u *User

	// Setup Heartbeat
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error { 
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil 
	})

	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for range ticker.C {
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil { return }
		}
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil { break }

		var p Packet
		if err := json.Unmarshal(msg, &p); err != nil { continue }

		userMu.Lock()
		switch p.Type {
		case "auth":
			u = handleAuth(p, conn)
		case "location":
			if u != nil {
				u.Lat, u.Lng = p.Lat, p.Lng
				broadcastLoc(u)
			}
		case "add_friend":
			if u != nil { handleFriendship(u, p.Target) }
		case "chat", "alert", "call_req", "call_accept", "call_decline", "call_hangup":
			if u != nil { relayStrict(u, p) }
		}
		userMu.Unlock()
	}
}

func handleAuth(p Packet, conn *websocket.Conn) *User {
	if exist, ok := users[p.Phone]; ok {
		if exist.Password == p.Pass {
			exist.Conn = conn
			conn.WriteJSON(Packet{Type: "auth_ok"})
			return exist
		}
		conn.WriteJSON(Packet{Type: "error", Text: "Auth Denied"})
		return nil
	}
	u := &User{Phone: p.Phone, Nickname: p.Nick, Password: p.Pass, Friends: make(map[string]bool), Conn: conn}
	users[p.Phone] = u
	conn.WriteJSON(Packet{Type: "auth_ok"})
	return u
}

func handleFriendship(u *User, target string) {
	if f, ok := users[target]; ok {
		u.Friends[target] = true
		f.Friends[u.Phone] = true
		u.Conn.WriteJSON(Packet{Type: "loc", Phone: f.Phone, Nick: f.Nickname, Lat: f.Lat, Lng: f.Lng})
		f.Conn.WriteJSON(Packet{Type: "loc", Phone: u.Phone, Nick: u.Nickname, Lat: u.Lat, Lng: u.Lng})
		u.Conn.WriteJSON(Packet{Type: "note", Text: "Linked with " + f.Nickname})
	}
}

func relayStrict(u *User, p Packet) {
	target, ok := users[p.Target]
	if !ok || target.Conn == nil {
		u.Conn.WriteJSON(Packet{Type: "note", Text: "Friend Offline"})
		return
	}

	p.From = u.Phone
	p.FromNick = u.Nickname

	// Track Call State for logic
	if p.Type == "call_req" { u.InCall = true }
	if p.Type == "call_hangup" { u.InCall = false; target.InCall = false }

	target.Conn.WriteJSON(p)
	if p.Type == "chat" { u.Conn.WriteJSON(p) }
}

func broadcastLoc(u *User) {
	for p := range u.Friends {
		if f, ok := users[p]; ok && f.Conn != nil {
			f.Conn.WriteJSON(Packet{Type: "loc", Phone: u.Phone, Nick: u.Nickname, Lat: u.Lat, Lng: u.Lng})
		}
	}
}
