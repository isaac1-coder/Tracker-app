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
	users     = make(map[string]*User)
	userMutex sync.RWMutex
	upgrader  = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
)

func main() {
	port := os.Getenv("PORT")
	if port == "" { port = "10000" }
	http.HandleFunc("/ws", handleWS)
	http.Handle("/", http.FileServer(http.Dir("./")))
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
				if u.Password == s { currentUser = u } else { userMutex.Unlock(); return }
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
				for p := range currentUser.Friends {
					if f, ok := users[p]; ok && f.Conn != nil {
						f.Conn.WriteJSON(map[string]interface{}{"type": "loc", "p": currentUser.Phone, "n": currentUser.Nickname, "lat": currentUser.Lat, "lng": currentUser.Lng})
					}
				}
			}
		case "add_friend":
			tgt := d["target"].(string)
			if f, ok := users[tgt]; ok && currentUser != nil {
				currentUser.Friends[tgt] = true
				f.Friends[currentUser.Phone] = true
				f.Conn.WriteJSON(map[string]interface{}{"type": "loc", "p": currentUser.Phone, "n": currentUser.Nickname, "lat": currentUser.Lat, "lng": currentUser.Lng})
				currentUser.Conn.WriteJSON(map[string]interface{}{"type": "loc", "p": f.Phone, "n": f.Nickname, "lat": f.Lat, "lng": f.Lng})
				runtime.GC()
			}
		case "chat", "alert", "call_signal":
			tgt := d["target"].(string)
			if f, ok := users[tgt]; ok && currentUser != nil {
				d["from"] = currentUser.Phone
				d["fromNick"] = currentUser.Nickname
				f.Conn.WriteJSON(d)
				if t == "chat" { currentUser.Conn.WriteJSON(d) }
			}
		}
		userMutex.Unlock()
	}
}
