package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
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
	Clients  map[*websocket.Conn]string
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var (
	logFile  *os.File
	logger   *log.Logger
	logMutex sync.Mutex
)

// Открытие файла логов при запуске программы
func init() {
	var err error
	logFile, err = os.OpenFile("application.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("Ошибка открытия файла логов: %v", err)
	}
	logger = log.New(logFile, "", log.LstdFlags)
}

// Обёртка для логирования и вывода
func logMessage(message string) {
	logMutex.Lock()
	defer logMutex.Unlock()

	// Выводим на консоль
	fmt.Println(message)

	// Пишем в файл
	logger.Println(message)
}

// Функция для мониторинга и логирования процесса, если он запущен
func (s *Server) monitorAndLogService(serviceName string) {
	logFileName := fmt.Sprintf("%s.log", serviceName)

	// Открываем или создаем лог-файл
	logFile, err := os.OpenFile(logFileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		logMessage(fmt.Sprintf("Ошибка открытия логов для %s: %v", serviceName, err))
		return
	}
	defer logFile.Close()

	multiWriter := io.MultiWriter(os.Stdout, logFile)

	// Команда для мониторинга вывода процесса (подключаемся к лог-файлу, не перезапуская процесс)
	cmd := exec.Command("tail", "-f", logFileName) // Используем tail -f для мониторинга логов
	cmd.Stdout = multiWriter
	cmd.Stderr = multiWriter

	go func() {
		err := cmd.Start()
		if err != nil {
			logMessage(fmt.Sprintf("Ошибка запуска tail для %s: %v", serviceName, err))
			return
		}
		cmd.Wait()
	}()
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

	if err != nil {
		return false, err
	}

	if out.Len() > 0 {
		return true, nil
	}
	return false, nil
}

func (s *Server) updateServiceStatus() {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	for i, service := range s.Services {
		running, err := isAppRunning(service.Name)
		if err != nil {
			s.Services[i].Status = "Не работает"
		} else if running {
			s.Services[i].Status = "Работает"
			logMessage(fmt.Sprintf("Сервис %s работает", service.Name))
		} else {
			s.Services[i].Status = "Неизвестно"
		}
	}
}

func (s *Server) startService(w http.ResponseWriter, r *http.Request) {
	serviceName := r.URL.Query().Get("name")

	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	// Проверяем статус службы
	for _, service := range s.Services {
		if service.Name == serviceName {
			if service.Status == "Работает" {
				logMessage(fmt.Sprintf("Сервис %s уже запущен", serviceName))
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
	}

	logFileName := fmt.Sprintf("%s.log", serviceName)

	// Обнуляем файл логов при старте
	err := os.Truncate(logFileName, 0)
	if err != nil && !os.IsNotExist(err) {
		logMessage(fmt.Sprintf("Ошибка обнуления логов для %s: %v", serviceName, err))
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Открываем файл для записи логов
	logFile, err := os.OpenFile(logFileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		logMessage(fmt.Sprintf("Ошибка открытия логов для %s: %v", serviceName, err))
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	multiWriter := io.MultiWriter(os.Stdout, logFile)

	// Запуск сервиса
	cmd := exec.Command("./" + serviceName)
	cmd.Stdout = multiWriter
	cmd.Stderr = multiWriter

	go func() {
		err := cmd.Start()
		if err != nil {
			logMessage(fmt.Sprintf("Ошибка запуска сервиса %s: %v", serviceName, err))
			logFile.Close()
			return
		}
		logMessage(fmt.Sprintf("Сервис %s запущен", serviceName))
		cmd.Wait()
		logMessage(fmt.Sprintf("Сервис %s остановлен", serviceName))
		logFile.Close()
	}()

	// Обновляем статус службы после запуска
	for i, service := range s.Services {
		if service.Name == serviceName {
			s.Services[i].Status = "Работает"
			break
		}
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) streamLogs(serviceName string, conn *websocket.Conn) {
	logFileName := fmt.Sprintf("%s.log", serviceName)
	file, err := os.Open(logFileName)
	if err != nil {
		logMessage(fmt.Sprintf("Ошибка открытия логов для %s: %v", serviceName, err))
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
			logMessage(fmt.Sprintf("Ошибка чтения логов для %s: %v", serviceName, err))
			break
		}
		message := string(line)
		err = conn.WriteMessage(websocket.TextMessage, []byte(message))
		if err != nil {
			logMessage(fmt.Sprintf("Ошибка отправки сообщения: %v", err))
			break
		}
	}
}

func (s *Server) stopService(w http.ResponseWriter, r *http.Request) {
	serviceName := r.URL.Query().Get("name")

	cmd := exec.Command("pkill", "-f", serviceName)
	err := cmd.Run()
	if err != nil {
		logMessage(fmt.Sprintf("Ошибка остановки сервиса %s: %v", serviceName, err))
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) restartService(w http.ResponseWriter, r *http.Request) {
	serviceName := r.URL.Query().Get("name")
	logMessage(fmt.Sprintf("Перезапуск сервиса: %s", serviceName))

	// Stop the service first
	cmd := exec.Command("pkill", "-f", serviceName)
	err := cmd.Run()
	if err != nil {
		logMessage(fmt.Sprintf("Ошибка остановки сервиса %s: %v", serviceName, err))
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Small delay to ensure the service has stopped fully
	time.Sleep(2 * time.Second)

	// Now start the service again
	s.startService(w, r)

	logMessage(fmt.Sprintf("Сервис %s перезапущен", serviceName))
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
		logMessage(fmt.Sprintf("Ошибка апгрейда соединения: %v", err))
		return
	}
	defer conn.Close()
	serviceName := r.URL.Query().Get("name")
	logMessage(fmt.Sprintf("Клиент подключился для логов сервиса %s", serviceName))
	s.Mutex.Lock()
	s.Clients[conn] = serviceName
	s.Mutex.Unlock()

	s.streamLogs(serviceName, conn)

	s.Mutex.Lock()
	delete(s.Clients, conn)
	s.Mutex.Unlock()
	logMessage(fmt.Sprintf("Клиент отключился от логов сервиса %s", serviceName))
}

func main() {
	config, err := loadConfig("config.toml")
	if err != nil {
		logMessage(fmt.Sprintf("Ошибка загрузки конфигурации: %v", err))
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

	logMessage("Запуск сервера на :8080")
	go func() {
		for {
			server.updateServiceStatus()
			time.Sleep(10 * time.Second) // Обновляем статус каждые 10 секунд
		}
	}()
	http.ListenAndServe(":8080", nil)
}
