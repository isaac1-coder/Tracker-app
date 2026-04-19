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

type User struct {
	Phone    string           `json:"phone"`
	Password string           `json:"password"`
	Lat      float64          `json:"lat"`
	Lng      float64          `json:"lng"`
	Friends  map[string]bool  `json:"-"`
	Conn     *websocket.Conn  `json:"-"`
	mu       sync.Mutex
}

var (
	users       = make(map[string]*User)
	userMutex   sync.RWMutex
	upgrader    = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	activeCallers = make(map[string]bool)
	callMutex   sync.Mutex
	callTimer   *time.Timer
)

func main() {
	port := os.Getenv("PORT")
	if port == "" { port = "10000" }

	http.HandleFunc("/ws", handleWebSocket)
	http.Handle("/", http.FileServer(http.Dir("./")))

	log.Printf("Server running on port %s", port)
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
		if err := json.Unmarshal(msg, &data); err != nil { continue }

		userMutex.Lock()
		msgType := data["type"].(string)

		switch msgType {
		case "auth":
			phone := data["phone"].(string)
			pass := data["pass"].(string)
			if u, ok := users[phone]; ok {
				if u.Password == pass {
					currentUser = u
				} else {
					conn.WriteJSON(map[string]string{"type": "error", "msg": "Wrong password"})
					userMutex.Unlock()
					return
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
			target := data["target"].(string)
			if f, ok := users[target]; ok && currentUser != nil {
				currentUser.Friends[target] = true
				f.Friends[currentUser.Phone] = true
				notify(f, "System", "New friend connected: "+currentUser.Phone)
				notify(currentUser, "System", "Added "+target)
			} else {
				notify(currentUser, "Error", "User not found")
			}

		case "chat":
			target := data["target"].(string)
			if f, ok := users[target]; ok && currentUser != nil {
				notify(f, currentUser.Phone, data["text"].(string))
			}

		case "call_request":
			handleCall(currentUser, data["target"].(string))
		}
		userMutex.Unlock()
	}
}

func handleCall(u *User, targetPhone string) {
	callMutex.Lock()
	defer callMutex.Unlock()

	if len(activeCallers) >= 2 {
		notify(u, "System", "Lines busy. Max 2 callers globally.")
		return
	}

	target := users[targetPhone]
	if target == nil { return }

	activeCallers[u.Phone] = true
	activeCallers[targetPhone] = true
	
	notify(target, "CALL", "Incoming call from "+u.Phone)
	notify(u, "CALL", "Call started with "+targetPhone)

	if callTimer != nil { callTimer.Stop() }
	callTimer = time.AfterFunc(3*time.Minute, func() {
		callMutex.Lock()
		activeCallers = make(map[string]bool)
		callMutex.Unlock()
		runtime.GC() // CLEAR RAM
		log.Println("Call timed out. Memory Purged.")
	})
}

func notify(u *User, from, msg string) {
	if u.Conn != nil {
		u.Conn.WriteJSON(map[string]string{"type": "msg", "from": from, "text": msg})
	}
}

func broadcastUpdate(u *User) {
	for phone := range u.Friends {
		if f, ok := users[phone]; ok && f.Conn != nil {
			f.Conn.WriteJSON(map[string]interface{}{
				"type": "loc_update",
				"phone": u.Phone,
				"lat": u.Lat,
				"lng": u.Lng,
			})
		}
	}
}
