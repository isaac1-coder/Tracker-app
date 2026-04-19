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
	Phone    string           `json:"phone"`
	Password string           `json:"password"`
	Lat      float64          `json:"lat"`
	Lng      float64          `json:"lng"`
	Friends  map[string]bool  `json:"-"`
	Conn     *websocket.Conn  `json:"-"`
}

var (
	users         = make(map[string]*User)
	userMutex     sync.RWMutex
	upgrader      = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	activeCallers = make(map[string]bool)
	callMutex     sync.Mutex
)

func main() {
	port := os.Getenv("PORT")
	if port == "" { port = "10000" }

	http.HandleFunc("/ws", handleWebSocket)
	http.Handle("/", http.FileServer(http.Dir("./")))

	log.Printf("Final Product running on port %s", port)
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
		msgType, _ := data["type"].(string)

		switch msgType {
		case "auth":
			phone := data["phone"].(string)
			pass := data["pass"].(string)
			if u, ok := users[phone]; ok {
				if u.Password == pass { currentUser = u } else {
					conn.WriteJSON(map[string]string{"type": "error", "msg": "Invalid Password"})
					userMutex.Unlock(); return
				}
			} else {
				currentUser = &User{Phone: phone, Password: pass, Friends: make(map[string]bool)}
				users[phone] = currentUser
			}
			currentUser.Conn = conn
			conn.WriteJSON(map[string]string{"type": "auth_success"})

		case "location":
			if currentUser != nil {
				currentUser.Lat, _ = data["lat"].(float64)
				currentUser.Lng, _ = data["lng"].(float64)
				broadcastUpdate(currentUser)
			}

		case "add_friend":
			targetPhone := data["target"].(string)
			if f, ok := users[targetPhone]; ok && currentUser != nil {
				currentUser.Friends[targetPhone] = true
				f.Friends[currentUser.Phone] = true
				// Immediate Sync
				sendLoc(f, currentUser)
				sendLoc(currentUser, f)
				notify(f, "System", currentUser.Phone + " added you!")
			}

		case "chat":
			target := data["target"].(string)
			if f, ok := users[target]; ok && currentUser != nil {
				f.Conn.WriteJSON(map[string]string{
					"type": "chat_msg", "from": currentUser.Phone, "text": data["text"].(string),
				})
			}

		case "call_init":
			target := data["target"].(string)
			callMutex.Lock()
			if len(activeCallers) >= 2 {
				notify(currentUser, "System", "Lines busy. Max 1 call allowed.")
			} else if f, ok := users[target]; ok {
				activeCallers[currentUser.Phone] = true
				activeCallers[target] = true
				f.Conn.WriteJSON(map[string]string{"type": "incoming_call", "from": currentUser.Phone})
				// 3 Minute Auto-Kill
				time.AfterFunc(3*time.Minute, func() {
					callMutex.Lock()
					delete(activeCallers, currentUser.Phone)
					delete(activeCallers, target)
					callMutex.Unlock()
					runtime.GC()
				})
			}
			callMutex.Unlock()
		}
		userMutex.Unlock()
	}
}

func sendLoc(to, about *User) {
	if to.Conn != nil {
		to.Conn.WriteJSON(map[string]interface{}{
			"type": "loc_update", "phone": about.Phone, "lat": about.Lat, "lng": about.Lng,
		})
	}
}

func notify(u *User, from, msg string) {
	if u.Conn != nil { u.Conn.WriteJSON(map[string]string{"type": "note", "msg": msg}) }
}

func broadcastUpdate(u *User) {
	for phone := range u.Friends {
		if f, ok := users[phone]; ok { sendLoc(f, u) }
	}
}
