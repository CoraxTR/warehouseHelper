package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/domain"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PGClient struct {
	Pool *pgxpool.Pool
}

func ConnString(cfg *config.PGConfig, dbName string) string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		cfg.PGHost,
		cfg.PGPort,
		cfg.PGUser,
		cfg.PGPassword,
		dbName,
	)
}

func NewPGClient(cfg *config.PGConfig) *PGClient {
	connString := ConnString(cfg, cfg.PGDatabase)

	pool, err := pgxpool.New(context.Background(), connString)
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = pool.Ping(ctx)
	if err != nil {
		panic(err)
	}

	return &PGClient{Pool: pool}
}

func (pg *PGClient) InsertOrders(ctx context.Context, orders []*domain.InternalOrder) error {
	if len(orders) == 0 {
		return nil
	}

	const query = `
INSERT INTO refgoOrders (
    href,
    name,
    receiver_name,
    receiver_phone_number,
    description,
    delivery_planned_date,
    shipment_address,
    delivery_interval_from,
    delivery_interval_until,
    delivery_region,
    payment_method,
    refgo_number,
    sum,
    chilled_weight,
    frozen_weight,
    frozen_boxes,
    chilled_boxes,
    errors
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10, $11, $12,
    $13, $14, $15, $16, $17, $18
) ON CONFLICT (href) DO NOTHING`

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return err
	}

	for _, o := range orders {
		errorsJSON, err := json.Marshal(o.GetErrors())
		if err != nil {
			err = tx.Rollback(ctx)
			if err != nil {
				return fmt.Errorf("transaction rollback failed: %w", err)
			}

			return err
		}

		_, err = tx.Exec(
			ctx,
			query,
			o.GetHREF(),
			o.GetName(),
			o.GetRecieverName(),
			o.GetRecieverPhoneNumber(),
			o.GetDescription(),
			o.GetDeliveryPlannedDate(),
			o.GetShipmentAddress(),
			o.GetDeliveryIntervalFrom(),
			o.GetDeliveryIntervalUntil(),
			o.GetDeliveryRegion(),
			o.GetPaymentMethod(),
			o.GetRefGoNumber(),
			o.GetSum(),
			o.GetChilledWeight(),
			o.GetFrozenWeight(),
			o.GetFrozenBoxes(),
			o.GetChilledBoxes(),
			errorsJSON,
		)
		if err != nil {
			err = tx.Rollback(ctx)
			if err != nil {
				return fmt.Errorf("transaction rollback failed: %w", err)
			}

			return err
		}
	}

	err = tx.Commit(ctx)
	if err != nil {
		err = tx.Rollback(ctx)
		if err != nil {
			return fmt.Errorf("transaction rollback failed: %w", err)
		}

		return err
	}

	return nil
}

func (pg *PGClient) GetAllOrders(ctx context.Context) ([]*domain.InternalOrder, error) {
	now := time.Now()
	tomorrow := now.AddDate(0, 0, 1).Format("02.01.2006")
	dayAfterTomorrow := now.AddDate(0, 0, 2).Format("02.01.2006")

	// Для отладки можно раскомментировать:
	// log.Printf("GetAllOrders: tomorrow=%s, dayAfterTomorrow=%s", tomorrow, dayAfterTomorrow)

	rows, err := pg.Pool.Query(ctx, `
        SELECT 
            href, name, receiver_name, receiver_phone_number, description,
            delivery_planned_date, shipment_address, delivery_interval_from,
            delivery_interval_until, delivery_region, payment_method, refgo_number,
            sum, chilled_weight, frozen_weight, frozen_boxes, chilled_boxes, errors
        FROM refgoOrders
        WHERE (delivery_planned_date = $1 AND (delivery_region = 'МСК' OR delivery_region IS NULL))
           OR (delivery_planned_date = $2 AND delivery_region = 'СПБ')
        ORDER BY refgo_number ASC
    `, tomorrow, dayAfterTomorrow)
	if err != nil {
		log.Printf("GetAllOrders query error: %v", err)

		return nil, err
	}
	defer rows.Close()

	var orders []*domain.InternalOrder

	for rows.Next() {
		var (
			href, name, receiverName, description, deliveryPlannedDate,
			shipmentAddress, deliveryIntervalFrom, deliveryIntervalUntil,
			deliveryRegion, paymentMethod, refgoNumber string
			receiverPhoneNumber, frozenBoxes, chilledBoxes uint64
			sum, chilledWeight, frozenWeight               float64
			errorsJSON                                     []byte
		)

		err := rows.Scan(
			&href, &name, &receiverName, &receiverPhoneNumber, &description,
			&deliveryPlannedDate, &shipmentAddress, &deliveryIntervalFrom,
			&deliveryIntervalUntil, &deliveryRegion, &paymentMethod, &refgoNumber,
			&sum, &chilledWeight, &frozenWeight, &frozenBoxes, &chilledBoxes,
			&errorsJSON,
		)
		if err != nil {
			log.Printf("GetAllOrders scan error: %v", err)

			return nil, err
		}

		order := &domain.InternalOrder{}
		order.SetHREF(href)
		order.SetName(name)
		order.SetRecieverName(receiverName)
		order.SetRecieverPhoneNumber(receiverPhoneNumber)
		order.SetDescription(description)
		order.SetDeliveryPlannedDate(deliveryPlannedDate)
		order.SetShipmentAddress(shipmentAddress)
		order.SetDeliveryIntervalFrom(deliveryIntervalFrom)
		order.SetDeliveryIntervalUntil(deliveryIntervalUntil)
		order.SetDeliveryRegion(deliveryRegion)
		order.SetPaymentMethod(paymentMethod)
		order.SetRefGoNumber(refgoNumber)
		order.SetSum(sum)
		order.SetChilledWeight(chilledWeight)
		order.SetFrozenWeight(frozenWeight)
		order.SetFrozenBoxes(frozenBoxes)
		order.SetChilledBoxes(chilledBoxes)

		if len(errorsJSON) > 0 {
			var errs map[string]string

			err = json.Unmarshal(errorsJSON, &errs)
			if err != nil {
				log.Printf("GetAllOrders unmarshal errors error: %v", err)

				return nil, err
			}

			order.SetErrors(errs)
		}

		orders = append(orders, order)
	}

	err = rows.Err()
	if err != nil {
		log.Printf("GetAllOrders rows error: %v", err)

		return nil, err
	}

	return orders, nil
}

func (pg *PGClient) UpdateOrders(ctx context.Context, orders []*domain.InternalOrder) error {
	if len(orders) == 0 {
		return nil
	}

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		err := tx.Rollback(ctx)
		if err != nil && !errors.Is(err, sql.ErrTxDone) {
			log.Printf("unexpected rollback error: %v", err)
		}
	}()

	const query = `
        UPDATE refgoOrders SET
            name = $1,
            receiver_name = $2,
            receiver_phone_number = $3,
            description = $4,
            delivery_planned_date = $5,
            shipment_address = $6,
            delivery_interval_from = $7,
            delivery_interval_until = $8,
            delivery_region = $9,
            payment_method = $10,
            sum = $11,
            chilled_weight = $12,
            frozen_weight = $13,
            frozen_boxes = $14,
            chilled_boxes = $15,
            errors = $16
        WHERE href = $17
    `

	for _, o := range orders {
		errorsJSON, err := json.Marshal(o.GetErrors())
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, query,
			o.GetName(),
			o.GetRecieverName(),
			o.GetRecieverPhoneNumber(),
			o.GetDescription(),
			o.GetDeliveryPlannedDate(),
			o.GetShipmentAddress(),
			o.GetDeliveryIntervalFrom(),
			o.GetDeliveryIntervalUntil(),
			o.GetDeliveryRegion(),
			o.GetPaymentMethod(),
			o.GetSum(),
			o.GetChilledWeight(),
			o.GetFrozenWeight(),
			o.GetFrozenBoxes(),
			o.GetChilledBoxes(),
			errorsJSON,
			o.GetHREF(),
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func (pg *PGClient) DeleteOrder(ctx context.Context, href string) error {
	_, err := pg.Pool.Exec(ctx, `DELETE FROM refgoOrders WHERE href = $1`, href)

	return err
}

func (pg *PGClient) GetOrdersByHREFs(ctx context.Context, hrefs []string) ([]*domain.InternalOrder, error) {
	if len(hrefs) == 0 {
		return nil, nil
	}

	rows, err := pg.Pool.Query(ctx, `
        SELECT 
            href, name, receiver_name, receiver_phone_number, description,
            delivery_planned_date, shipment_address, delivery_interval_from,
            delivery_interval_until, delivery_region, payment_method, refgo_number,
            sum, chilled_weight, frozen_weight, frozen_boxes, chilled_boxes, errors
        FROM refgoOrders
        WHERE href = ANY($1)
        ORDER BY refgo_number::integer ASC
    `, hrefs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []*domain.InternalOrder

	for rows.Next() {
		var (
			href, name, receiverName, description, deliveryPlannedDate,
			shipmentAddress, deliveryIntervalFrom, deliveryIntervalUntil,
			deliveryRegion, paymentMethod, refgoNumber string
			receiverPhoneNumber, frozenBoxes, chilledBoxes uint64
			sum, chilledWeight, frozenWeight               float64
			errorsJSON                                     []byte
		)

		err := rows.Scan(
			&href, &name, &receiverName, &receiverPhoneNumber, &description,
			&deliveryPlannedDate, &shipmentAddress, &deliveryIntervalFrom,
			&deliveryIntervalUntil, &deliveryRegion, &paymentMethod, &refgoNumber,
			&sum, &chilledWeight, &frozenWeight, &frozenBoxes, &chilledBoxes,
			&errorsJSON,
		)
		if err != nil {
			log.Printf("GetAllOrders scan error: %v", err)

			return nil, err
		}

		order := &domain.InternalOrder{}
		order.SetHREF(href)
		order.SetName(name)
		order.SetRecieverName(receiverName)
		order.SetRecieverPhoneNumber(receiverPhoneNumber)
		order.SetDescription(description)
		order.SetDeliveryPlannedDate(deliveryPlannedDate)
		order.SetShipmentAddress(shipmentAddress)
		order.SetDeliveryIntervalFrom(deliveryIntervalFrom)
		order.SetDeliveryIntervalUntil(deliveryIntervalUntil)
		order.SetDeliveryRegion(deliveryRegion)
		order.SetPaymentMethod(paymentMethod)
		order.SetRefGoNumber(refgoNumber)
		order.SetSum(sum)
		order.SetChilledWeight(chilledWeight)
		order.SetFrozenWeight(frozenWeight)
		order.SetFrozenBoxes(frozenBoxes)
		order.SetChilledBoxes(chilledBoxes)

		if len(errorsJSON) > 0 {
			var errs map[string]string

			err := json.Unmarshal(errorsJSON, &errs)
			if err != nil {
				log.Printf("GetAllOrders unmarshal errors error: %v", err)

				return nil, err
			}

			order.SetErrors(errs)
		}

		orders = append(orders, order)
	}

	err = rows.Err()
	if err != nil {
		log.Printf("GetAllOrders rows error: %v", err)

		return nil, err
	}

	return orders, nil
}
