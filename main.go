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
	users         = make(map[string]*User)
	userMutex     sync.RWMutex
	upgrader      = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	activeCall    = "" // Stores CallerID
	callMutex     sync.Mutex
)

func main() {
	port := os.Getenv("PORT")
	if port == "" { port = "10000" }

	http.HandleFunc("/ws", handleWS)
	http.Handle("/", http.FileServer(http.Dir("./")))

	log.Printf("SnapTracker Pro Online on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil { return }
	defer conn.Close()

	var currentUser *User

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil { break }

		var d map[string]interface{}
		json.Unmarshal(msg, &d)
		t, _ := d["type"].(string)

		userMutex.Lock()
		switch t {
		case "auth":
			p, n, s := d["phone"].(string), d["nick"].(string), d["pass"].(string)
			if u, ok := users[p]; ok {
				if u.Password == s { currentUser = u } else {
					conn.WriteJSON(map[string]string{"type": "error", "msg": "Invalid Pass"})
					userMutex.Unlock(); return
				}
			} else {
				currentUser = &User{Phone: p, Nickname: n, Password: s, Friends: make(map[string]bool)}
				users[p] = currentUser
			}
			currentUser.Conn = conn
			conn.WriteJSON(map[string]string{"type": "auth_ok"})

		case "location":
			if currentUser != nil {
				currentUser.Lat, _ = d["lat"].(float64)
				currentUser.Lng, _ = d["lng"].(float64)
				broadcastLoc(currentUser)
			}

		case "add_friend":
			tgt := d["target"].(string)
			if f, ok := users[tgt]; ok && currentUser != nil {
				currentUser.Friends[tgt] = true
				f.Friends[currentUser.Phone] = true
				syncPair(currentUser, f)
			}

		case "chat":
			tgt, txt := d["target"].(string), d["text"].(string)
			if f, ok := users[tgt]; ok {
				m := map[string]string{"type": "chat", "from": currentUser.Phone, "nick": currentUser.Nickname, "text": txt}
				f.Conn.WriteJSON(m)
				currentUser.Conn.WriteJSON(m)
			}

		case "alert":
			if f, ok := users[d["target"].(string)]; ok {
				f.Conn.WriteJSON(map[string]string{"type": "ring_alert", "from": currentUser.Nickname})
			}

		case "call_init":
			tgt := d["target"].(string)
			callMutex.Lock()
			if activeCall != "" {
				currentUser.Conn.WriteJSON(map[string]string{"type": "note", "msg": "Line Busy"})
			} else if f, ok := users[tgt]; ok {
				activeCall = currentUser.Phone
				f.Conn.WriteJSON(map[string]string{"type": "incoming_call", "from": currentUser.Nickname, "phone": currentUser.Phone})
			}
			callMutex.Unlock()

		case "call_accept":
			cID := d["caller"].(string)
			if caller, ok := users[cID]; ok {
				msg := map[string]string{"type": "call_live", "with": currentUser.Nickname}
				caller.Conn.WriteJSON(msg)
				currentUser.Conn.WriteJSON(msg)
				// Auto-kill in 3 mins
				time.AfterFunc(3*time.Minute, endCallGlobal)
			}

		case "call_hangup":
			endCallGlobal()
		}
		userMutex.Unlock()
	}
}

func endCallGlobal() {
	callMutex.Lock()
	activeCall = ""
	callMutex.Unlock()
	for _, u := range users {
		if u.Conn != nil { u.Conn.WriteJSON(map[string]string{"type": "call_ended"}) }
	}
	runtime.GC()
}

func broadcastLoc(u *User) {
	for p := range u.Friends {
		if f, ok := users[p]; ok {
			f.Conn.WriteJSON(map[string]interface{}{"type": "loc", "p": u.Phone, "n": u.Nickname, "lat": u.Lat, "lng": u.Lng})
		}
	}
}

func syncPair(a, b *User) {
	a.Conn.WriteJSON(map[string]interface{}{"type": "loc", "p": b.Phone, "n": b.Nickname, "lat": b.Lat, "lng": b.Lng})
	b.Conn.WriteJSON(map[string]interface{}{"type": "loc", "p": a.Phone, "n": a.Nickname, "lat": a.Lat, "lng": a.Lng})
	a.Conn.WriteJSON(map[string]string{"type": "note", "msg": "Added " + b.Nickname})
}
