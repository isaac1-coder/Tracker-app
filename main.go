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

// Data Structures
type User struct {
	Phone    string  `json:"phone"`
	Password string  `json:"password"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Friends  []string `json:"friends"`
	Conn     *websocket.Conn
}

var (
	users       = make(map[string]*User)
	userMutex   sync.Mutex
	upgrader    = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	
	// Global Call Lock (Only 2 people in the entire app can call)
	activeCallers []string
	callMutex     sync.Mutex
	callTimer     *time.Timer
)

func main() {
	port := os.Getenv("PORT")
	if port == "" { port = "10000" }

	http.HandleFunc("/ws", handleWebSocket)
	http.Handle("/", http.FileServer(http.Dir("./")))

	fmt.Printf("Server starting on port %s...\n", port)
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
		switch data["type"] {
		case "auth":
			phone := data["phone"].(string)
			pass := data["pass"].(string)
			if u, ok := users[phone]; ok {
				if u.Password == pass { currentUser = u }
			} else {
				users[phone] = &User{Phone: phone, Password: pass, Friends: []string{}}
				currentUser = users[phone]
			}
			if currentUser != nil { currentUser.Conn = conn }

		case "location":
			if currentUser != nil {
				currentUser.Lat = data["lat"].(float64)
				currentUser.Lng = data["lng"].(float64)
				broadcastToFriends(currentUser, "update", data)
			}

		case "add_friend":
			target := data["target"].(string)
			if f, ok := users[target]; ok && currentUser != nil {
				f.Friends = append(f.Friends, currentUser.Phone)
				currentUser.Friends = append(currentUser.Friends, f.Phone)
				notify(f, "New Friend Request accepted!")
			}

		case "chat":
			target := data["target"].(string)
			if f, ok := users[target]; ok {
				notify(f, fmt.Sprintf("Message from %s: %s", currentUser.Phone, data["text"]))
			}

		case "call_request":
			handleCallRequest(currentUser, data["target"].(string))
		}
		userMutex.Unlock()
	}
}

func handleCallRequest(u *User, targetPhone string) {
	callMutex.Lock()
	defer callMutex.Unlock()

	if len(activeCallers) > 0 {
		notify(u, "App-wide call limit reached (max 2 users).")
		return
	}

	target := users[targetPhone]
	if target == nil { return }

	activeCallers = []string{u.Phone, targetPhone}
	notify(target, "Incoming Call from "+u.Phone)
	
	// Start 3-minute limit
	if callTimer != nil { callTimer.Stop() }
	callTimer = time.AfterFunc(3*time.Minute, endCall)
}

func endCall() {
	callMutex.Lock()
	activeCallers = nil
	callMutex.Unlock()
	
	// FORCED RAM CLEARING: Trigger GC as requested to keep Render instance tiny
	runtime.GC()
	log.Println("Call ended. RAM cleared.")
}

func notify(u *User, msg string) {
	if u.Conn != nil {
		u.Conn.WriteJSON(map[string]string{"type": "notification", "msg": msg})
	}
}

func broadcastToFriends(u *User, t string, data interface{}) {
	for _, fPhone := range u.Friends {
		if f, ok := users[fPhone]; ok && f.Conn != nil {
			f.Conn.WriteJSON(map[string]interface{}{"type": t, "data": data})
		}
	}
}
