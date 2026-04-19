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
	users       = make(map[string]*User)
	userMutex   sync.RWMutex
	upgrader    = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	activeCallers = make(map[string]bool)
	callMutex   sync.Mutex
)

func main() {
	port := os.Getenv("PORT")
	if port == "" { port = "10000" }

	http.HandleFunc("/ws", handleWebSocket)
	http.Handle("/", http.FileServer(http.Dir("./")))

	log.Printf("Server online on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil { return }
	defer conn.Close()

	var currentUser *User

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil { break }

		var data map[string]interface{}
		json.Unmarshal(msg, &data)

		userMutex.Lock()
		mType, _ := data["type"].(string)

		switch mType {
		case "auth":
			p, nick, pass := data["phone"].(string), data["nick"].(string), data["pass"].(string)
			if u, ok := users[p]; ok {
				if u.Password == pass { currentUser = u } else {
					conn.WriteJSON(map[string]string{"type": "error", "msg": "Wrong Pass"})
					userMutex.Unlock(); return
				}
			} else {
				currentUser = &User{Phone: p, Nickname: nick, Password: pass, Friends: make(map[string]bool)}
				users[p] = currentUser
			}
			currentUser.Conn = conn
			conn.WriteJSON(map[string]string{"type": "auth_success"})

		case "location":
			if currentUser != nil {
				currentUser.Lat, _ = data["lat"].(float64)
				currentUser.Lng, _ = data["lng"].(float64)
				for fP := range currentUser.Friends {
					if f, ok := users[fP]; ok { sendLoc(f, currentUser) }
				}
			}

		case "add_friend":
			target := data["target"].(string)
			if f, ok := users[target]; ok && currentUser != nil {
				currentUser.Friends[target] = true
				f.Friends[currentUser.Phone] = true
				sendLoc(f, currentUser); sendLoc(currentUser, f)
				notify(f, "System", currentUser.Nickname+" added you!")
			}

		case "chat":
			target := data["target"].(string)
			if f, ok := users[target]; ok && currentUser != nil {
				msgObj := map[string]string{"type": "chat_msg", "fromPhone": currentUser.Phone, "fromNick": currentUser.Nickname, "text": data["text"].(string)}
				f.Conn.WriteJSON(msgObj)
				currentUser.Conn.WriteJSON(msgObj)
			}

		case "alert": // RING DEVICE
			target := data["target"].(string)
			if f, ok := users[target]; ok {
				f.Conn.WriteJSON(map[string]string{"type": "ring", "from": currentUser.Nickname})
			}

		case "call_request":
			target := data["target"].(string)
			callMutex.Lock()
			if len(activeCallers) >= 2 {
				notify(currentUser, "System", "Line Busy")
			} else if f, ok := users[target]; ok {
				activeCallers[currentUser.Phone] = true
				activeCallers[target] = true
				f.Conn.WriteJSON(map[string]string{"type": "incoming_call", "from": currentUser.Nickname, "phone": currentUser.Phone})
			}
			callMutex.Unlock()
		}
		userMutex.Unlock()
	}
}

func sendLoc(to, about *User) {
	if to.Conn != nil {
		to.Conn.WriteJSON(map[string]interface{}{
			"type": "loc_update", "phone": about.Phone, "nick": about.Nickname, "lat": about.Lat, "lng": about.Lng,
		})
	}
}

func notify(u *User, from, msg string) {
	if u.Conn != nil { u.Conn.WriteJSON(map[string]string{"type": "note", "from": from, "msg": msg}) }
}
