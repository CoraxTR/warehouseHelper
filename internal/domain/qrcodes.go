package domain

import "time"

// QRPhoto — фотография кода маркировки, привязанная к заказу.
type QRPhoto struct {
	ID        string    // id фото, генерируется приложением; имя файла QRCodes/<id>.<ext>
	Ext       string    // расширение файла без точки: jpg, png, webp
	CreatedAt time.Time // время сохранения (заполняет репозиторий из БД)
}

// QROrder — заказ с фотографиями кодов маркировки.
type QROrder struct {
	ID          int64
	OrderNumber string
	Photos      []QRPhoto
}
