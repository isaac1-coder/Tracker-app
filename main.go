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
// DATA DEFINITIONS & MODELS
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

type MessagePacket struct {
	Type     string  `json:"type"`
	From     string  `json:"from,omitempty"`
	FromNick string  `json:"fromNick,omitempty"`
	Target   string  `json:"target,omitempty"`
	Text     string  `json:"text,omitempty"`
	Lat      float64 `json:"lat,omitempty"`
	Lng      float64 `json:"lng,omitempty"`
	Phone    string  `json:"phone,omitempty"`
	Nick     string  `json:"nick,omitempty"`
	MsgID    string  `json:"msgId,omitempty"`
	ReplyTo  string  `json:"replyTo,omitempty"`
	Accept   bool    `json:"accept,omitempty"`
	Msg      string  `json:"msg,omitempty"`
}

// ---------------------------------------------------------------- //
// GLOBAL STATE & CONTEXT
// ---------------------------------------------------------------- //

var (
	registry      = make(map[string]*User)
	registryMutex sync.RWMutex
	upgrader      = websocket.Upgrader{
		ReadBufferSize:  2048,
		WriteBufferSize: 2048,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}
	// Call Synchronization
	callLock sync.Mutex
	callState = struct {
		IsBusy   bool
		Caller   string
		Target   string
		StartsAt time.Time
	}{IsBusy: false}
)

// ---------------------------------------------------------------- //
// CORE ENGINE
// ---------------------------------------------------------------- //

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}

	// Startup Routine
	go callMonitor()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handleClient)
	mux.Handle("/", http.FileServer(http.Dir("./")))

	log.Printf("[BOOT] SnapTracker Professional starting on :%s", port)
	
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("[CRITICAL] Server failed: %v", err)
	}
}

func handleClient(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ERR] WebSocket Upgrade: %v", err)
		return
	}
	defer conn.Close()

	var sessionUser *User

	for {
		mt, raw, err := conn.ReadMessage()
		if err != nil {
			if sessionUser != nil {
				sessionUser.Online = false
				sessionUser.Conn = nil
			}
			break
		}

		// BINARY VOIP PIPELINE
		// This captures 1-second chunks and relays them through RAM
		if mt == websocket.BinaryMessage {
			if sessionUser != nil {
				relayVOIP(sessionUser, raw)
			}
			continue
		}

		// JSON SIGNALING
		var pkg MessagePacket
		if err := json.Unmarshal(raw, &pkg); err != nil {
			continue
		}

		registryMutex.Lock()
		processSignal(pkg, conn, &sessionUser)
		registryMutex.Unlock()
	}
}

// ---------------------------------------------------------------- //
// SIGNAL PROCESSING
// ---------------------------------------------------------------- //

func processSignal(p MessagePacket, conn *websocket.Conn, session **User) {
	switch p.Type {
	case "auth":
		*session = handleAuth(p, conn)
	case "location":
		if *session != nil {
			updateLocation(*session, p)
		}
	case "add_friend":
		if *session != nil {
			linkFriends(*session, p)
		}
	case "chat":
		if *session != nil {
			relayChat(*session, p)
		}
	case "react":
		if *session != nil {
			relayReaction(*session, p)
		}
	case "alert":
		if *session != nil {
			relayAlert(*session, p)
		}
	case "call_invite":
		if *session != nil {
			initiateCall(*session, p)
		}
	case "call_response":
		if *session != nil {
			finalizeCall(*session, p)
		}
	case "transcription":
		if *session != nil {
			relayTranscription(*session, p)
		}
	}
}

// ---------------------------------------------------------------- //
// HANDLER LOGIC
// ---------------------------------------------------------------- //

func handleAuth(p MessagePacket, conn *websocket.Conn) *User {
	u, ok := registry[p.Phone]
	if ok {
		if u.Password == p.Text {
			u.Conn = conn
			u.Online = true
			conn.WriteJSON(MessagePacket{Type: "auth_ok"})
			return u
		}
		conn.WriteJSON(MessagePacket{Type: "error", Msg: "Credentials mismatch"})
		return nil
	}
	newUser := &User{
		Phone:    p.Phone,
		Nickname: p.Nick,
		Password: p.Text,
		Friends:  make(map[string]bool),
		Conn:     conn,
		Online:   true,
	}
	registry[p.Phone] = newUser
	conn.WriteJSON(MessagePacket{Type: "auth_ok"})
	runtime.GC()
	return newUser
}

func updateLocation(u *User, p MessagePacket) {
	u.Lat, u.Lng = p.Lat, p.Lng
	for phone := range u.Friends {
		if f, ok := registry[phone]; ok && f.Conn != nil {
			f.Conn.WriteJSON(MessagePacket{
				Type:  "loc",
				Phone: u.Phone,
				Nick:  u.Nickname,
				Lat:   u.Lat,
				Lng:   u.Lng,
			})
		}
	}
}

func relayChat(u *User, p MessagePacket) {
	if f, ok := registry[p.Target]; ok {
		p.From = u.Phone
		p.FromNick = u.Nickname
		f.Conn.WriteJSON(p)
		u.Conn.WriteJSON(p)
	}
}

func relayVOIP(u *User, data []byte) {
	for phone := range u.Friends {
		if f, ok := registry[phone]; ok && f.Conn != nil {
			f.Conn.WriteMessage(websocket.BinaryMessage, data)
		}
	}
}

func initiateCall(u *User, p MessagePacket) {
	callLock.Lock()
	defer callLock.Unlock()

	if callState.IsBusy {
		u.Conn.WriteJSON(MessagePacket{Type: "note", Msg: "Global call limit reached (max 1)"})
		return
	}

	if tgt, ok := registry[p.Target]; ok {
		callState.IsBusy = true
		callState.Caller = u.Phone
		callState.Target = p.Target
		callState.StartsAt = time.Now()
		tgt.Conn.WriteJSON(MessagePacket{Type: "call_invite", From: u.Phone, FromNick: u.Nickname})
	}
}

func finalizeCall(u *User, p MessagePacket) {
	if !p.Accept {
		callLock.Lock()
		callState.IsBusy = false
		callLock.Unlock()
	}
	if caller, ok := registry[p.Target]; ok {
		caller.Conn.WriteJSON(p)
	}
}

func relayAlert(u *User, p MessagePacket) {
	if tgt, ok := registry[p.Target]; ok {
		tgt.Conn.WriteJSON(MessagePacket{Type: "ring_alert", From: u.Nickname})
	}
}

func relayTranscription(u *User, p MessagePacket) {
	if tgt, ok := registry[p.Target]; ok {
		tgt.Conn.WriteJSON(p)
	}
}

func linkFriends(u *User, p MessagePacket) {
	tgt, ok := registry[p.Target]
	if !ok {
		u.Conn.WriteJSON(MessagePacket{Type: "note", Msg: "Phone not found"})
		return
	}
	u.Friends[p.Target] = true
	tgt.Friends[u.Phone] = true
	u.Conn.WriteJSON(MessagePacket{Type: "loc", Phone: tgt.Phone, Nick: tgt.Nickname, Lat: tgt.Lat, Lng: tgt.Lng})
	tgt.Conn.WriteJSON(MessagePacket{Type: "loc", Phone: u.Phone, Nick: u.Nickname, Lat: u.Lat, Lng: u.Lng})
}

func relayReaction(u *User, p MessagePacket) {
	if tgt, ok := registry[p.Target]; ok {
		tgt.Conn.WriteJSON(MessagePacket{Type: "react", From: u.Phone, MsgID: p.MsgID})
	}
}

func callMonitor() {
	for {
		time.Sleep(15 * time.Second)
		callLock.Lock()
		if callState.IsBusy && time.Since(callState.StartsAt) > (3*time.Minute) {
			callState.IsBusy = false
			log.Println("[TIMER] Call session force-closed.")
			runtime.GC()
		}
		callLock.Unlock()
	}
}
