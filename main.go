package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync"

	"github.com/gorilla/websocket"
)

type User struct {
	Phone    string          `json:"phone"`
	Nickname string          `json:"nickname"`
	Password string          `json:"password"`
	Lat      float64         `json:"lat"`
	Lng      float64         `json:"lng"`
	Friends  map[string]bool `json:"-"`
	Conn     *websocket.Conn `json:"-"`
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
	MsgID    string  `json:"msgId,omitempty"`
	ReplyTo  string  `json:"replyTo,omitempty"`
	Accept   bool    `json:"accept,omitempty"`
}

var (
	registry = make(map[string]*User)
	regMu    sync.RWMutex
	upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
)

func main() {
	port := os.Getenv("PORT")
	if port == "" { port = "10000" }

	http.HandleFunc("/ws", handleWS)
	http.Handle("/", http.FileServer(http.Dir("./")))

	log.Printf("Titan Server Online: %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil { return }
	defer conn.Close()

	var u *User

	for {
		mt, msg, err := conn.ReadMessage()
		if err != nil { break }

		if mt == websocket.BinaryMessage && u != nil {
			relayAudio(u, msg)
			continue
		}

		var p Packet
		if err := json.Unmarshal(msg, &p); err != nil { continue }

		regMu.Lock()
		switch p.Type {
		case "auth":
			u = handleAuth(p, conn)
		case "location":
			if u != nil {
				u.Lat, u.Lng = p.Lat, p.Lng
				broadcastLoc(u)
			}
		case "add_friend":
			if u != nil { handleFriend(u, p.Target) }
		case "chat", "alert", "call_invite", "call_resp", "transcription":
			if u != nil { relayPacket(u, p) }
		}
		regMu.Unlock()
	}
}

func handleAuth(p Packet, conn *websocket.Conn) *User {
	if u, ok := registry[p.Phone]; ok {
		if u.Password == p.Text {
			u.Conn = conn
			conn.WriteJSON(Packet{Type: "auth_ok"})
			return u
		}
		return nil
	}
	u := &User{Phone: p.Phone, Nickname: p.Nick, Password: p.Text, Friends: make(map[string]bool), Conn: conn}
	registry[p.Phone] = u
	conn.WriteJSON(Packet{Type: "auth_ok"})
	runtime.GC()
	return u
}

func handleFriend(u *User, target string) {
	if f, ok := registry[target]; ok {
		u.Friends[target] = true
		f.Friends[u.Phone] = true
		f.Conn.WriteJSON(Packet{Type: "loc", Phone: u.Phone, Nick: u.Nickname, Lat: u.Lat, Lng: u.Lng})
		u.Conn.WriteJSON(Packet{Type: "loc", Phone: f.Phone, Nick: f.Nickname, Lat: f.Lat, Lng: f.Lng})
	}
}

func relayPacket(u *User, p Packet) {
	if f, ok := registry[p.Target]; ok {
		p.From = u.Phone
		p.FromNick = u.Nickname
		f.Conn.WriteJSON(p)
		if p.Type == "chat" { u.Conn.WriteJSON(p) }
	}
}

func relayAudio(u *User, data []byte) {
	for p := range u.Friends {
		if f, ok := registry[p]; ok && f.Conn != nil {
			f.Conn.WriteMessage(websocket.BinaryMessage, data)
		}
	}
}

func broadcastLoc(u *User) {
	for p := range u.Friends {
		if f, ok := registry[p]; ok && f.Conn != nil {
			f.Conn.WriteJSON(Packet{Type: "loc", Phone: u.Phone, Nick: u.Nickname, Lat: u.Lat, Lng: u.Lng})
		}
	}
}
