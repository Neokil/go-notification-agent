package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

const SocketName = "/tmp/go-notification-agent.sock"

type NotificationUrgency uint8

func (n NotificationUrgency) String() string {
	switch n {
	case Low:
		return "Low"
	case Normal:
		return "Normal"
	case High:
		return "High"
	default:
		return fmt.Sprintf("Invalid NotificationUrgency: %d", n)
	}
}

const (
	Low    NotificationUrgency = 0
	Normal NotificationUrgency = 1
	High   NotificationUrgency = 2
)

type Notification struct {
	Title     string
	Message   string
	Urgency   NotificationUrgency
	CreatedOn time.Time
}

var notifications = []Notification{}
var notificationsMutex = sync.RWMutex{}

var colorBarBackground string = "#000000"
var colorBarText string = "#FFFFFF"
var colorBarTextUrgent string = "#FF0000"

func main() {
	colorBarBackground, _ = GetXrdbValue("background")
	colorBarText, _ = GetXrdbValue("foreground-alt")
	colorBarTextUrgent, _ = GetXrdbValue("secondary")

	go listenForNotification()
	go listenToSocket()
	printNotifications()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	<-c
}

func listenToSocket() {

	os.Remove(SocketName)
	l, err := net.Listen("unix", SocketName)
	if err != nil {
		panic(err)
	}
	defer l.Close()

	for {
		con, err := l.Accept()
		if err != nil {
			panic(err)
		}
		go handleConnection(con)
	}
}

func handleConnection(c net.Conn) {
	r := bufio.NewReader(c)
	s, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		panic(err)
	}
	s = strings.TrimSpace(s)
	switch s {
	case "pop":
		notificationsMutex.Lock()
		if len(notifications) > 0 {
			notifications = notifications[0 : len(notifications)-1]
		}
		notificationsMutex.Unlock()
		_, _ = c.Write([]byte("Ok"))
	case "clear":
		notificationsMutex.Lock()
		if len(notifications) > 0 {
			notifications = []Notification{}
		}
		notificationsMutex.Unlock()
		_, _ = c.Write([]byte("Ok"))
	case "get-list":
		notificationsMutex.RLock()
		b, err := json.Marshal(notifications)
		notificationsMutex.RUnlock()
		if err != nil {
			_, _ = c.Write([]byte("Error: " + err.Error()))
		}
		_, _ = c.Write(b)
	case "exit":
		_, _ = c.Write([]byte("Ok"))
		os.Exit(0)
	default:
		_, _ = c.Write([]byte("Error: Unknown Command"))
	}

	_, _ = c.Write([]byte("\n"))
	c.Close()
	printNotifications()
}

func printSeparator() {
	fmt.Printf(" %%{F%s}%%{T2}%%{F%s}%%{T-} ", colorBarBackground, colorBarText)
}

func printNotifications() {
	notificationsMutex.RLock()
	defer notificationsMutex.RUnlock()

	fmt.Printf("%d ", len(notifications))
	if len(notifications) == 0 {
		fmt.Print("\n")
		return
	}
	printSeparator()

	for i, n := range notifications {
		if i > 0 {
			printSeparator()
		}
		if i >= 3 {
			break
		}
		text := n.Title + ": " + n.Message
		if len(text) > 40 {
			text = text[0:39] + "..."
		}
		if n.Urgency == High {
			text = fmt.Sprintf("%%{F%s}%s%%{F%s}", colorBarTextUrgent, text, colorBarText)
		}
		fmt.Print(text)
	}
	if len(notifications) > 3 {
		fmt.Printf(" +%d", (len(notifications) - 3))
	}
	fmt.Print("\n")
}

func listenForNotification() {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to connect to session bus:", err)
		os.Exit(1)
	}
	defer conn.Close()

	var rules = []string{
		"type='signal',member='Notify',path='/org/freedesktop/Notifications',interface='org.freedesktop.Notifications'",
		"type='method_call',member='Notify',path='/org/freedesktop/Notifications',interface='org.freedesktop.Notifications'",
		"type='method_return',member='Notify',path='/org/freedesktop/Notifications',interface='org.freedesktop.Notifications'",
		"type='error',member='Notify',path='/org/freedesktop/Notifications',interface='org.freedesktop.Notifications'",
	}
	var flag uint = 0

	call := conn.BusObject().Call("org.freedesktop.DBus.Monitoring.BecomeMonitor", 0, rules, flag)
	if call.Err != nil {
		fmt.Fprintln(os.Stderr, "Failed to become monitor:", call.Err)
		os.Exit(1)
	}

	c := make(chan *dbus.Message, 10)
	conn.Eavesdrop(c)
	for v := range c {
		if len(v.Body) < 7 {
			continue
		}
		props := v.Body[6].(map[string]dbus.Variant)
		urgency := Low
		urgencyVariant, ok := props["urgency"]
		if ok {
			urgency = NotificationUrgency(urgencyVariant.Value().(uint8))
		}

		notificationsMutex.Lock()
		notifications = append([]Notification{{
			Title:     v.Body[3].(string),
			Message:   v.Body[4].(string),
			Urgency:   urgency,
			CreatedOn: time.Now(),
		}}, notifications...)
		notificationsMutex.Unlock()
		printNotifications()
	}
}

func GetXrdbValue(name string) (string, error) {
	cmd := exec.Command("xrdb", "-get", name)

	stderr := &strings.Builder{}
	cmd.Stderr = stderr

	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("Failed to get value from xrdb:%s: %w", stderr.String(), err)
	}
	return strings.TrimSpace(string(b)), nil
}
