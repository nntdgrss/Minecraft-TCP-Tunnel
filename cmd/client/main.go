package main

import (
"bufio"
"flag"
"fmt"
"io"
"log"
"net"
"os"
"strings"
"sync"
"time"

"github.com/nntdgrs/minecraft-tcp-tunnel/internal/tunnel"
)

var (
serverHost    = flag.String("host", "localhost", "Адрес VDS сервера")
serverPort    = flag.Int("port", 25566, "Порт для туннельного соединения на сервере")
minecraftPort = flag.Int("mcport", 25565, "Порт локального Minecraft сервера")
)

type Client struct {
serverConn net.Conn
mu         sync.RWMutex
channels   map[uint32]net.Conn
}

func NewClient() *Client {
return &Client{
channels: make(map[uint32]net.Conn),
}
}

func (c *Client) addChannel(id uint32, conn net.Conn) {
c.mu.Lock()
c.channels[id] = conn
c.mu.Unlock()
}

func (c *Client) removeChannel(id uint32) {
c.mu.Lock()
if conn, ok := c.channels[id]; ok {
conn.Close()
delete(c.channels, id)
}
c.mu.Unlock()
}

func (c *Client) getChannel(id uint32) (net.Conn, bool) {
c.mu.RLock()
defer c.mu.RUnlock()
conn, ok := c.channels[id]
return conn, ok
}

func main() {
flag.Parse()
client := NewClient()

for {
serverConn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", *serverHost, *serverPort))
if err != nil {
log.Printf("Ошибка подключения к серверу: %v", err)
time.Sleep(5 * time.Second)
continue
}
log.Printf("Подключено к серверу %s", serverConn.RemoteAddr())

if err := client.authenticate(serverConn); err != nil {
log.Printf("Ошибка авторизации: %v", err)
serverConn.Close()
time.Sleep(5 * time.Second)
continue
}

client.serverConn = serverConn
client.handleTunnelConnection()

log.Printf("Соединение потеряно. Переподключение через 5 секунд...")
time.Sleep(5 * time.Second)
}
}

func (c *Client) authenticate(conn net.Conn) error {
// Устанавливаем таймаут на операции авторизации
conn.SetDeadline(time.Now().Add(30 * time.Second))
defer conn.SetDeadline(time.Time{}) // Сбрасываем таймаут после авторизации

// Получаем ключ авторизации от сервера
packet, err := tunnel.ReadPacket(conn)
if err != nil {
return fmt.Errorf("ошибка при чтении ключа: %v", err)
}

if packet.Type != tunnel.AuthKey {
return fmt.Errorf("неожиданный тип пакета при авторизации")
}

log.Printf("Получен ключ авторизации от сервера")

// Запрашиваем ключ у пользователя
reader := bufio.NewReader(os.Stdin)
fmt.Print("Введите ключ авторизации: ")
input, err := reader.ReadString('\n')
if err != nil {
return fmt.Errorf("ошибка при чтении ввода: %v", err)
}

// Отправляем введенный ключ
input = strings.TrimSpace(input)
if err := tunnel.WriteAuthPacket(conn, []byte(input)); err != nil {
return fmt.Errorf("ошибка при отправке ключа: %v", err)
}

// Получаем ответ
response, err := tunnel.ReadPacket(conn)
if err != nil {
return fmt.Errorf("ошибка при чтении ответа: %v", err)
}

if response.Type != tunnel.AuthResponse {
return fmt.Errorf("неожиданный тип пакета в ответе")
}

if len(response.Data) == 0 || response.Data[0] == 0 {
return fmt.Errorf("неверный ключ авторизации")
}

log.Printf("Авторизация успешна")
return nil
}

func (c *Client) handleTunnelConnection() {
defer func() {
c.serverConn.Close()
// Закрываем все активные каналы
c.mu.Lock()
for _, conn := range c.channels {
conn.Close()
}
c.channels = make(map[uint32]net.Conn)
c.mu.Unlock()
}()

// Создаем канал для критических ошибок туннеля
errChan := make(chan error, 1)

for {
packet, err := tunnel.ReadPacket(c.serverConn)
if err != nil {
if err == io.EOF {
log.Printf("Сервер закрыл соединение")
} else {
log.Printf("Ошибка при чтении из туннеля: %v", err)
}
return
}

switch packet.Type {
case tunnel.TunnelData:
// Проверяем, существует ли уже соединение для этого канала
c.mu.RLock()
mcConn, exists := c.channels[packet.ChannelID]
c.mu.RUnlock()

if !exists {
// Создаем новое соединение с локальным Minecraft сервером
mcConn, err = net.Dial("tcp", fmt.Sprintf("localhost:%d", *minecraftPort))
if err != nil {
log.Printf("Ошибка подключения к Minecraft серверу: %v", err)
continue
}

c.addChannel(packet.ChannelID, mcConn)
go c.handleMinecraftConnection(packet.ChannelID, mcConn)
}

// Отправляем данные в соединение Minecraft
_, err = mcConn.Write(packet.Data)
if err != nil {
log.Printf("Ошибка при отправке данных Minecraft серверу: %v", err)
c.removeChannel(packet.ChannelID)
continue
}

case tunnel.ChannelClosed:
log.Printf("Канал %d закрыт сервером", packet.ChannelID)
c.removeChannel(packet.ChannelID)

default:
log.Printf("Получен неожиданный тип пакета: %v", packet.Type)
}

// Проверяем наличие критических ошибок
select {
case err := <-errChan:
if err != io.EOF && !isConnectionClosed(err) {
log.Printf("Критическая ошибка в туннеле: %v", err)
return
}
default:
}
}
}

// isConnectionClosed проверяет, является ли ошибка результатом закрытия соединения
func isConnectionClosed(err error) bool {
if err == nil {
return false
}
return strings.Contains(err.Error(), "use of closed network connection") ||
strings.Contains(err.Error(), "connection reset by peer") ||
strings.Contains(err.Error(), "broken pipe")
}

func (c *Client) handleMinecraftConnection(channelID uint32, mcConn net.Conn) {
defer c.removeChannel(channelID)

buffer := make([]byte, 8192)
for {
n, err := mcConn.Read(buffer)
if err != nil {
if err != io.EOF {
log.Printf("Канал %d: ошибка при чтении от Minecraft сервера: %v", channelID, err)
}
return
}

err = tunnel.WritePacket(c.serverConn, channelID, buffer[:n])
if err != nil {
if err != io.EOF && !isConnectionClosed(err) {
log.Printf("Канал %d: ошибка при отправке в туннель: %v", channelID, err)
}
return
}
}
}
