package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gorilla/websocket"
)

type Config struct {
	Namespace      string
	Comment        string
	Version        string
	IpDevice       string
	StationASDU    int `toml:"Station_ASDU"`
	UpportTcp      int `toml:"Upport_tcp"`
	LocalPort      int `toml:"Local_port"`
	CountTtySerial int `toml:"Count_tty_serial"`
	CountTcpSerial int `toml:"Count_tcp_serial"`
	DeadZone       int
	Delta          int
	IEC101         []IEC101Config
	IEC104         []IEC104Config
	Modbus         []ModbusConfig
}

type IEC101Config struct {
	IdPU       int
	Comment    string
	TtyPort    string `toml:"Tty_port"`
	Baud       int
	StopBit    int `toml:"Stop_bit"`
	CountBit   int `toml:"Count_bit"`
	TimeLoop   int `toml:"Time_loop"`
	Parity     string
	Diagnostic int
}

type IEC104Config struct {
	IdPU       int
	Comment    string
	TcpPort    int    `toml:"Tcp_port"`
	IpAddress  string `toml:"Ip_address"`
	Diagnostic int
}

type ModbusConfig struct {
	Comment    string
	PortTty    string `toml:"Port_tty"`
	TimeLoop   int    `toml:"Time_loop"`
	IPAddress  string `toml:"IP_adress"`
	Port       string
	Mode       string
	Baud       int
	Parity     string
	Diagnostic int
	Stop       int
	Bits       int
	AddressId  int `toml:"Address_id"`
}

type Service struct {
	Name    string
	Comment string
	Status  string
}

type Server struct {
	Config   Config
	Services []Service
	Mutex    sync.Mutex
	Clients  map[*websocket.Conn]string // Карта клиентов и соответствующих им служб
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func loadConfig(filename string) (Config, error) {
	var config Config
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return config, fmt.Errorf("config file not found: %s", filename)
	}
	if _, err := toml.DecodeFile(filename, &config); err != nil {
		return config, fmt.Errorf("error decoding config file: %v", err)
	}
	return config, nil
}
func isAppRunning(appName string) (bool, error) {
	cmd := exec.Command("pgrep", "-f", appName)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()

	// Для отладки
	fmt.Printf("Checking if app %s is running...\n", appName)
	if err != nil {
		fmt.Printf("Error executing pgrep for app %s: %v\n", appName, err)
		return false, err
	}

	if out.Len() > 0 {
		fmt.Printf("App %s is running.\n", appName)
		return true, nil
	}

	fmt.Printf("App %s is not running.\n", appName)
	return false, nil
}

func (s *Server) updateServiceStatus() {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	fmt.Println("Updating service status...")
	for i, service := range s.Services {
		running, err := isAppRunning(service.Name)
		if err != nil {
			fmt.Printf("Error checking status of service %s: %v\n", service.Name, err)
			s.Services[i].Status = "Unknown"
		} else if running {
			s.Services[i].Status = "Running"
		} else {
			s.Services[i].Status = "Stopped"
		}
	}
}

func (s *Server) startService(w http.ResponseWriter, r *http.Request) {
	serviceName := r.URL.Query().Get("name")
	fmt.Printf("Starting service: %s\n", serviceName)

	logFileName := fmt.Sprintf("%s.log", serviceName)
	logFile, err := os.OpenFile(logFileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Printf("Error creating/opening log file for service %s: %v\n", serviceName, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	multiWriter := io.MultiWriter(os.Stdout, logFile)

	cmd := exec.Command("./" + serviceName)
	cmd.Stdout = multiWriter
	cmd.Stderr = multiWriter

	go func() {
		err := cmd.Start()
		if err != nil {
			fmt.Printf("Error starting service %s: %v\n", serviceName, err)
			logFile.Close()
			return
		}
		fmt.Printf("Service %s started\n", serviceName)
		cmd.Wait()
		fmt.Printf("Service %s stopped\n", serviceName)
		logFile.Close()
	}()
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) streamLogs(serviceName string, conn *websocket.Conn) {
	logFileName := fmt.Sprintf("%s.log", serviceName)
	file, err := os.Open(logFileName)
	if err != nil {
		fmt.Printf("Error opening log file for service %s: %v\n", serviceName, err)
		return
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				time.Sleep(1 * time.Second)
				continue
			}
			fmt.Printf("Error reading log for service %s: %v\n", serviceName, err)
			break
		}
		message := string(line)
		fmt.Printf("Log for %s: %s", serviceName, message)
		err = conn.WriteMessage(websocket.TextMessage, []byte(message))
		if err != nil {
			fmt.Println("Write message error:", err)
			break
		}
	}
}

func (s *Server) stopService(w http.ResponseWriter, r *http.Request) {
	serviceName := r.URL.Query().Get("name")
	fmt.Printf("Stopping service: %s\n", serviceName)

	cmd := exec.Command("pkill", "-f", serviceName)
	err := cmd.Run()
	if err != nil {
		fmt.Printf("Error stopping service %s: %v\n", serviceName, err)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) restartService(w http.ResponseWriter, r *http.Request) {
	serviceName := r.URL.Query().Get("name")
	fmt.Printf("Restarting service: %s\n", serviceName)
	s.stopService(w, r)
	s.startService(w, r)
}

func (s *Server) homeHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("index.html"))
	s.Mutex.Lock()
	defer s.Mutex.Unlock()
	tmpl.Execute(w, s.Services)
}

func (s *Server) statusHandler(w http.ResponseWriter, r *http.Request) {
	s.updateServiceStatus()
	s.Mutex.Lock()
	defer s.Mutex.Unlock()
	json.NewEncoder(w).Encode(s.Services)
}

func (s *Server) logHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println("Upgrade error:", err)
		return
	}
	defer conn.Close()
	serviceName := r.URL.Query().Get("name")
	fmt.Printf("Client connected for logs of %s\n", serviceName)
	s.Mutex.Lock()
	s.Clients[conn] = serviceName
	s.Mutex.Unlock()

	s.streamLogs(serviceName, conn)

	s.Mutex.Lock()
	delete(s.Clients, conn)
	s.Mutex.Unlock()
	fmt.Printf("Client disconnected for logs of %s\n", serviceName)
}

func main() {
	config, err := loadConfig("config.toml")
	if err != nil {
		fmt.Printf("Ошибка загрузки конфигурации: %v\n", err)
		return
	}

	var services []Service
	for _, iec101 := range config.IEC101 {
		services = append(services, Service{Name: "IEC101", Comment: iec101.Comment})
	}
	for _, iec104 := range config.IEC104 {
		services = append(services, Service{Name: "IEC104", Comment: iec104.Comment})
	}
	for i, modbus := range config.Modbus {
		services = append(services, Service{Name: fmt.Sprintf("Modbus%d", i+1), Comment: modbus.Comment})
	}

	server := &Server{
		Config:   config,
		Services: services,
		Clients:  make(map[*websocket.Conn]string),
	}

	http.HandleFunc("/", server.homeHandler)
	http.HandleFunc("/status", server.statusHandler)
	http.HandleFunc("/start", server.startService)
	http.HandleFunc("/stop", server.stopService)
	http.HandleFunc("/restart", server.restartService)
	http.HandleFunc("/ws", server.logHandler)

	fmt.Println("Starting server on :8080")
	go func() {
		for {
			server.updateServiceStatus()
			time.Sleep(10 * time.Second) // Обновляем статус каждые 10 секунд
		}
	}()
	http.ListenAndServe(":8080", nil)
}
