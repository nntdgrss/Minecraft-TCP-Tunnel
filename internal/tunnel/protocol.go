package tunnel

import (
"encoding/binary"
"io"
)

const (
// Размер заголовка: 1 байт тип + 4 байта для ID канала + 4 байта для длины данных
HeaderSize = 9
// Максимальный размер пакета
MaxPacketSize = 1024 * 1024 // 1MB
// Длина ключа авторизации
AuthKeyLength = 6
)

// MessageType определяет тип сообщения в туннеле
type MessageType byte

const (
AuthKey MessageType = iota + 1    // Отправка ключа авторизации
AuthResponse                       // Ответ на авторизацию
TunnelData                        // Данные туннеля
ChannelClosed                     // Уведомление о закрытии канала
)

// Packet представляет собой структуру пакета в туннеле
type Packet struct {
Type      MessageType
ChannelID uint32
Data      []byte
}

// writeHeader записывает заголовок пакета
func writeHeader(w io.Writer, msgType MessageType, channelID uint32, dataLen uint32) error {
if err := binary.Write(w, binary.BigEndian, msgType); err != nil {
return err
}
if err := binary.Write(w, binary.BigEndian, channelID); err != nil {
return err
}
if err := binary.Write(w, binary.BigEndian, dataLen); err != nil {
return err
}
return nil
}

// WriteAuthPacket отправляет пакет авторизации
func WriteAuthPacket(w io.Writer, data []byte) error {
return writePacket(w, AuthKey, 0, data)
}

// WriteAuthResponse отправляет ответ на авторизацию
func WriteAuthResponse(w io.Writer, success bool) error {
var data []byte
if success {
data = []byte{1}
} else {
data = []byte{0}
}
return writePacket(w, AuthResponse, 0, data)
}

// WritePacket записывает пакет данных в соединение
func WritePacket(w io.Writer, channelID uint32, data []byte) error {
return writePacket(w, TunnelData, channelID, data)
}

// WriteChannelClosed отправляет уведомление о закрытии канала
func WriteChannelClosed(w io.Writer, channelID uint32) error {
return writePacket(w, ChannelClosed, channelID, nil)
}

// writePacket записывает любой пакет в соединение
func writePacket(w io.Writer, msgType MessageType, channelID uint32, data []byte) error {
if err := writeHeader(w, msgType, channelID, uint32(len(data))); err != nil {
return err
}
if len(data) > 0 {
_, err := w.Write(data)
return err
}
return nil
}

// ReadPacket читает пакет из соединения
func ReadPacket(r io.Reader) (*Packet, error) {
var msgType MessageType
if err := binary.Read(r, binary.BigEndian, &msgType); err != nil {
return nil, err
}

var channelID uint32
if err := binary.Read(r, binary.BigEndian, &channelID); err != nil {
return nil, err
}

var length uint32
if err := binary.Read(r, binary.BigEndian, &length); err != nil {
return nil, err
}

if length > MaxPacketSize {
return nil, io.ErrShortBuffer
}

var data []byte
if length > 0 {
data = make([]byte, length)
if _, err := io.ReadFull(r, data); err != nil {
return nil, err
}
}

return &Packet{
Type:      msgType,
ChannelID: channelID,
Data:      data,
}, nil
}
