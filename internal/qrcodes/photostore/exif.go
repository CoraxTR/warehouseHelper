package photostore

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
)

// jpegOrientation возвращает EXIF-ориентацию JPEG (тег 0x0112 в IFD0) или 1,
// если тега нет, файл повреждён или это не JPEG. Go-декодеры ориентацию не
// применяют, а браузеры при показе оригинала — применяют; поэтому уменьшенная
// копия должна учитывать поворот, иначе фото, снятое телефоном «на боку»,
// в копии ляжет набок.
func jpegOrientation(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 1
	}
	defer func() { _ = f.Close() }()

	r := bufio.NewReader(f)
	if b, err := r.ReadByte(); err != nil || b != 0xFF {
		return 1
	}
	if b, err := r.ReadByte(); err != nil || b != 0xD8 { // SOI
		return 1
	}

	for {
		m, err := nextMarker(r)
		if err != nil {
			return 1
		}
		// SOS — дальше энтропийные данные; EXIF бывает только до них.
		if m == 0xDA {
			return 1
		}
		// Длина сегмента (2 байта BE, включает сами байты длины).
		var ln [2]byte
		if _, err := io.ReadFull(r, ln[:]); err != nil {
			return 1
		}
		size := int(binary.BigEndian.Uint16(ln[:]))
		if size < 2 {
			return 1
		}
		payload := make([]byte, size-2)
		if _, err := io.ReadFull(r, payload); err != nil {
			return 1
		}
		// APP1 с EXIF: «Exif\0\0» + TIFF.
		if m == 0xE1 && len(payload) >= 14 && string(payload[:6]) == "Exif\x00\x00" {
			return tiffOrientation(payload[6:])
		}
	}
}

// nextMarker читает очередной маркер JPEG, пропуская fill-байты, повторные
// FF и встроенные FF 00. Возвращает код маркера без FF.
func nextMarker(r *bufio.Reader) (byte, error) {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if b != 0xFF {
			continue
		}
		for {
			b, err := r.ReadByte()
			if err != nil {
				return 0, err
			}
			if b == 0xFF {
				continue
			}
			if b == 0x00 {
				break // встроенный FF — ищем маркер заново
			}
			return b, nil
		}
	}
}

// tiffOrientation извлекает ориентацию из TIFF-блока EXIF (IFD0, тег 0x0112).
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(t[2:4]) != 42 {
		return 1
	}
	ifd0 := int(order.Uint32(t[4:8]))
	if ifd0+2 > len(t) {
		return 1
	}
	count := int(order.Uint16(t[ifd0 : ifd0+2]))
	for i := range count {
		// Запись IFD: tag(2) type(2) count(4) value(4).
		e := ifd0 + 2 + i*12
		if e+12 > len(t) {
			return 1
		}
		tag := order.Uint16(t[e : e+2])
		typ := order.Uint16(t[e+2 : e+4])
		if tag != 0x0112 || typ != 3 { // Orientation: SHORT.
			continue
		}
		o := int(order.Uint16(t[e+8 : e+10]))
		if o < 1 || o > 8 {
			return 1
		}
		return o
	}
	return 1
}
