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

	log.Printf("Zenith Server Online: %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil { return }
	defer conn.Close()

	var u *User

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil { break }

		var d map[string]interface{}
		if err := json.Unmarshal(msg, &d); err != nil { continue }
		t, _ := d["type"].(string)

		regMu.Lock()
		switch t {
		case "auth":
			p, n, s := d["phone"].(string), d["nick"].(string), d["pass"].(string)
			if exist, ok := registry[p]; ok {
				if exist.Password == s {
					u = exist
					u.Conn = conn
					conn.WriteJSON(map[string]string{"type": "auth_ok"})
				} else {
					conn.WriteJSON(map[string]string{"type": "error", "msg": "Wrong Pass"})
				}
			} else {
				u = &User{Phone: p, Nickname: n, Password: s, Friends: make(map[string]bool), Conn: conn}
				registry[p] = u
				conn.WriteJSON(map[string]string{"type": "auth_ok"})
			}
		case "location":
			if u != nil {
				u.Lat, u.Lng = d["lat"].(float64), d["lng"].(float64)
				broadcastLoc(u)
			}
		case "add_friend":
			if u != nil {
				if f, ok := registry[d["target"].(string)]; ok {
					u.Friends[f.Phone] = true
					f.Friends[u.Phone] = true
					syncPair(u, f)
				}
			}
		case "chat", "react", "alert", "webrtc_signal":
			if u != nil {
				if f, ok := registry[d["target"].(string)]; ok && f.Conn != nil {
					d["from"] = u.Phone
					d["fromNick"] = u.Nickname
					f.Conn.WriteJSON(d)
					if t == "chat" || t == "react" { u.Conn.WriteJSON(d) }
				}
			}
		}
		regMu.Unlock()
	}
}

func broadcastLoc(u *User) {
	for p := range u.Friends {
		if f, ok := registry[p]; ok && f.Conn != nil {
			f.Conn.WriteJSON(map[string]interface{}{"type": "loc", "p": u.Phone, "n": u.Nickname, "lat": u.Lat, "lng": u.Lng})
		}
	}
}

func syncPair(a, b *User) {
	a.Conn.WriteJSON(map[string]interface{}{"type": "loc", "p": b.Phone, "n": b.Nickname, "lat": b.Lat, "lng": b.Lng})
	b.Conn.WriteJSON(map[string]interface{}{"type": "loc", "p": a.Phone, "n": a.Nickname, "lat": a.Lat, "lng": a.Lng})
	runtime.GC()
}
