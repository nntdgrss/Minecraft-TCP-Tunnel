package main

import (
"flag"
"fmt"
"io"
"log"
"math/rand"
"net"
"strings"
"sync"
"sync/atomic"
"time"

"github.com/nntdgrs/minecraft-tcp-tunnel/internal/tunnel"
)

var (
listenPort = flag.Int("port", 25565, "Port to listen for Minecraft clients")
tunnelPort = flag.Int("tunnel", 25566, "Port for tunnel connection from local client")
)

type Server struct {
nextChannelID uint32
tunnelConn    net.Conn
mcListener    net.Listener
mu            sync.RWMutex
tunnelMu      sync.RWMutex
channels      map[uint32]net.Conn
closeOnce     sync.Once
}

func NewServer(mcListener net.Listener) *Server {
return &Server{
channels:   make(map[uint32]net.Conn),
mcListener: mcListener,
}
}

func (s *Server) addChannel(conn net.Conn) uint32 {
id := atomic.AddUint32(&s.nextChannelID, 1)
s.mu.Lock()
s.channels[id] = conn
s.mu.Unlock()
return id
}

func (s *Server) removeChannel(id uint32) {
s.mu.Lock()
if conn, ok := s.channels[id]; ok {
conn.Close()
delete(s.channels, id)
if tunnelConn := s.tunnelConn; tunnelConn != nil {
tunnel.WriteChannelClosed(tunnelConn, id)
}
}
s.mu.Unlock()
}

func (s *Server) getChannel(id uint32) (net.Conn, bool) {
s.mu.RLock()
defer s.mu.RUnlock()
conn, ok := s.channels[id]
return conn, ok
}

func (s *Server) getTunnelConn() net.Conn {
s.tunnelMu.RLock()
defer s.tunnelMu.RUnlock()
return s.tunnelConn
}

func (s *Server) setTunnelConn(conn net.Conn) {
s.tunnelMu.Lock()
s.tunnelConn = conn
s.tunnelMu.Unlock()
}

// isConnectionClosed проверяет, является ли ошибка результатом закрытия соединения
func isConnectionClosed(err error) bool {
if err == nil {
return false
}
return err == io.EOF ||
strings.Contains(err.Error(), "use of closed network connection") ||
strings.Contains(err.Error(), "connection reset by peer") ||
strings.Contains(err.Error(), "broken pipe")
}

// generateAuthKey генерирует случайный ключ для авторизации
func generateAuthKey() string {
const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
key := make([]byte, tunnel.AuthKeyLength)
for i := range key {
key[i] = charset[rand.Intn(len(charset))]
}
return string(key)
}

func main() {
flag.Parse()
rand.Seed(time.Now().UnixNano())

// Создаем основной слушатель для Minecraft клиентов
mcListener, err := net.Listen("tcp", fmt.Sprintf(":%d", *listenPort))
if err != nil {
log.Fatalf("Ошибка при создании Minecraft слушателя: %v", err)
}
defer mcListener.Close()
log.Printf("Minecraft слушатель запущен на порту %d", *listenPort)

// Создаем слушатель для туннельных соединений
tunnelListener, err := net.Listen("tcp", fmt.Sprintf(":%d", *tunnelPort))
if err != nil {
log.Fatalf("Ошибка при создании туннельного слушателя: %v", err)
}
defer tunnelListener.Close()
log.Printf("Ожидание туннельного соединения на порту %d...", *tunnelPort)

// Создаем сервер
server := NewServer(mcListener)

// Запускаем обработчик Minecraft подключений
go server.acceptMinecraftClients()

// Основной цикл приема туннельных соединений
for {
tunnelConn, err := tunnelListener.Accept()
if err != nil {
log.Printf("Ошибка при принятии туннельного соединения: %v", err)
continue
}
log.Printf("Новое туннельное соединение от %s", tunnelConn.RemoteAddr())

// Создаем новую сессию для туннельного соединения
server.handleTunnelConnection(tunnelConn)
}
}

func (s *Server) acceptMinecraftClients() {
for {
mcConn, err := s.mcListener.Accept()
if err != nil {
log.Printf("Ошибка при принятии Minecraft соединения: %v", err)
continue
}

if s.getTunnelConn() == nil {
log.Printf("Нет активного туннельного соединения, отключаем клиента %s", mcConn.RemoteAddr())
mcConn.Close()
continue
}

log.Printf("Новое подключение от Minecraft клиента: %s", mcConn.RemoteAddr())
go s.handleMinecraftClient(mcConn)
}
}

func (s *Server) handleTunnelConnection(tunnelConn net.Conn) {
// Генерируем ключ авторизации
authKey := generateAuthKey()
log.Printf("Ключ авторизации: %s", authKey)

// Отправляем ключ клиенту
if err := tunnel.WriteAuthPacket(tunnelConn, []byte(authKey)); err != nil {
log.Printf("Ошибка при отправке ключа авторизации: %v", err)
tunnelConn.Close()
return
}

// Ждем ответ с ключом
packet, err := tunnel.ReadPacket(tunnelConn)
if err != nil {
log.Printf("Ошибка при чтении ответа авторизации: %v", err)
tunnelConn.Close()
return
}

if packet.Type != tunnel.AuthKey || string(packet.Data) != authKey {
log.Printf("Неверный ключ авторизации")
tunnel.WriteAuthResponse(tunnelConn, false)
tunnelConn.Close()
return
}

// Авторизация успешна
if err := tunnel.WriteAuthResponse(tunnelConn, true); err != nil {
log.Printf("Ошибка при отправке ответа авторизации: %v", err)
tunnelConn.Close()
return
}

log.Printf("Клиент авторизован успешно")

// Закрываем предыдущее соединение
if prevConn := s.getTunnelConn(); prevConn != nil {
prevConn.Close()
}

// Устанавливаем новое соединение
s.setTunnelConn(tunnelConn)

// Читаем пакеты из туннеля
for {
packet, err := tunnel.ReadPacket(tunnelConn)
if err != nil {
if isConnectionClosed(err) {
log.Printf("Туннель закрыт клиентом")
} else {
log.Printf("Ошибка при чтении из туннеля: %v", err)
}
break
}

switch packet.Type {
case tunnel.TunnelData:
mcConn, ok := s.getChannel(packet.ChannelID)
if !ok {
continue
}

if _, err = mcConn.Write(packet.Data); err != nil {
log.Printf("Ошибка при отправке данных Minecraft клиенту: %v", err)
s.removeChannel(packet.ChannelID)
}

case tunnel.ChannelClosed:
s.removeChannel(packet.ChannelID)

default:
log.Printf("Получен неожиданный тип пакета: %v", packet.Type)
}
}

// Закрываем туннель и очищаем ссылку
tunnelConn.Close()

// Закрываем все активные каналы перед удалением туннеля
s.mu.Lock()
for id := range s.channels {
s.removeChannel(id)
}
s.mu.Unlock()

s.setTunnelConn(nil)
}

func (s *Server) handleMinecraftClient(mcConn net.Conn) {
defer mcConn.Close()

// Создаем новый канал для клиента
channelID := s.addChannel(mcConn)
defer s.removeChannel(channelID)

// Создаем буфер для чтения
buffer := make([]byte, 8192)

for {
n, err := mcConn.Read(buffer)
if err != nil {
if !isConnectionClosed(err) {
log.Printf("Ошибка при чтении от Minecraft клиента: %v", err)
}
return
}

tunnelConn := s.getTunnelConn()
if tunnelConn == nil {
log.Printf("Туннель недоступен, отключаем клиента")
return
}

if err = tunnel.WritePacket(tunnelConn, channelID, buffer[:n]); err != nil {
if !isConnectionClosed(err) {
log.Printf("Ошибка при отправке в туннель: %v", err)
}
return
}
}
}
