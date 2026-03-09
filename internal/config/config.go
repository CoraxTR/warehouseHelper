package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	*MoySkladConfig
	*RefGoConfig
	*PGConfig
}

func LoadConfig() *Config {
	err := godotenv.Load("../.env")
	if err != nil {
		panic("Cannot read config file")
	}

	msc := loadMoySkladConfig()
	rfc := loadRefGoConfig()
	pfc := loadPGConfig()

	if os.Getenv("RG_LATESTORDER") == "" {
		panic("RG_LATESTORDER does not exist")
	}

	return &Config{
		MoySkladConfig: msc,
		RefGoConfig:    rfc,
		PGConfig:       pfc,
	}
}

type MoySkladConfig struct {
	Hrefs *MoySkladhrefs

	APIKEY        string
	TimeSpan      time.Duration
	RequestCap    int
	SellTypeID    string
	RefGoNumberID string
	CourierID     string
	TimeFormat    string
	URLstart      string
	AuthHeader    string
	EncodeHeader  string
}

func loadMoySkladConfig() *MoySkladConfig {
	mshrf := loadMoySkladhrefs()

	apiKey := os.Getenv("MSAPI_KEY")
	if apiKey == "" {
		panic("API KEY does not exist")
	}

	tspnint, err := strconv.Atoi(os.Getenv("MSAPI_REQUESTCAPTIMESPAN"))
	if err != nil {
		panic("Timespan does not exist")
	}

	tspn := time.Duration(int64(tspnint)) * time.Second

	rqcap, err := strconv.Atoi(os.Getenv("MSAPI_REQUESTCAP"))
	if err != nil {
		panic("Requestcap does not exist")
	}

	selltypeID := os.Getenv("MSAPI_SELLTYPEID")
	if selltypeID == "" {
		panic("SelltypeID does not exist")
	}

	refgonumberid := os.Getenv("MSAPI_REFGONUMBERID")
	if refgonumberid == "" {
		panic("RefGoNumberID does not exist")
	}

	courierid := os.Getenv("MSAPI_COURIERID")
	if courierid == "" {
		panic("CourierID does not exist")
	}

	timeFormat := os.Getenv("MSAPI_TIMEFORMAT")
	if timeFormat == "" {
		panic("Timeformat does not exist")
	}

	urlstart := os.Getenv("MSAPI_URLSTART")
	if urlstart == "" {
		panic("URLstart does not exist")
	}

	authheader := os.Getenv("MSAPI_AUTHHEADER")
	if authheader == "" {
		panic("AuthHeader does not exist")
	}

	encodeheader := os.Getenv("MSAPI_ENCODEHEADER")
	if encodeheader == "" {
		panic("AuthHeader does not exist")
	}

	return &MoySkladConfig{
		Hrefs: mshrf,

		APIKEY:        apiKey,
		TimeSpan:      tspn,
		RequestCap:    rqcap,
		SellTypeID:    selltypeID,
		RefGoNumberID: refgonumberid,
		CourierID:     courierid,
		TimeFormat:    timeFormat,
		URLstart:      urlstart,
		AuthHeader:    authheader,
		EncodeHeader:  encodeheader,
	}
}

type MoySkladhrefs struct {
	Readystatehref    string
	Shipedstatehref   string
	SellTypehref      string
	SellTypeOtherhref string
	Storehref         string
	Orghref           string
	RefGoNumberhref   string
	Courierhref       string
	RefGoCourierhref  string
}

func loadMoySkladhrefs() *MoySkladhrefs {
	readystatehref := os.Getenv("MSAPI_READYSTATEHREF")
	if readystatehref == "" {
		panic("Statehref does not exist")
	}

	shipedstatehref := os.Getenv("MSAPI_SHIPEDSTATEHREF")
	if shipedstatehref == "" {
		panic("Shipedstatehref does not exist")
	}

	selltypehref := os.Getenv("MSAPI_SELLTYPEHREF")
	if selltypehref == "" {
		panic("Selltype does not exist")
	}

	selltypeOtherhref := os.Getenv("MSAPI_SELLTYPEOTHERHREF")
	if selltypeOtherhref == "" {
		panic("OtherSelltype does not exist")
	}

	storehref := os.Getenv("MSAPI_STOREHREF")
	if storehref == "" {
		panic("Storehref does not exist")
	}

	orghref := os.Getenv("MSAPI_ORGHREF")
	if orghref == "" {
		panic("Orghref does not exist")
	}

	refgonumberhref := os.Getenv("MSAPI_REFGONUMBERHREF")
	if refgonumberhref == "" {
		panic("RefGoNumberhref does not exist")
	}

	courierhref := os.Getenv("MSAPI_COURIERHREF")
	if courierhref == "" {
		panic("Courierhref does not exist")
	}

	refgocourierhref := os.Getenv("MSAPI_REFGOCOURIERHREF")
	if refgocourierhref == "" {
		panic("RefGoCourierhref does not exist")
	}

	return &MoySkladhrefs{
		Readystatehref:    readystatehref,
		Shipedstatehref:   shipedstatehref,
		SellTypehref:      selltypehref,
		SellTypeOtherhref: selltypeOtherhref,
		Storehref:         storehref,
		Orghref:           orghref,
		RefGoNumberhref:   refgonumberhref,
		Courierhref:       courierhref,
		RefGoCourierhref:  refgocourierhref,
	}
}

type RefGoConfig struct {
	RGNextOrder int
}

func loadRefGoConfig() *RefGoConfig {
	if os.Getenv("RG_LATESTORDER") == "" {
		panic("RG_LATESTORDER does not exist")
	}

	latestorder, err := strconv.Atoi(strings.Trim(os.Getenv("RG_LATESTORDER"), `"`))
	if err != nil {
		panic("Invalid RG_LATESTORDER")
	}

	return &RefGoConfig{
		RGNextOrder: latestorder,
	}
}

func ChangeRefGoLatest(latestOrder int) error {
	envFile := "../.env"

	content, err := os.ReadFile(envFile)
	if err != nil {
		return fmt.Errorf("ошибка чтения файла: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	found := false

	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "RG_LATESTORDER=") {
			lines[i] = fmt.Sprintf("RG_LATESTORDER=\"%d\"", latestOrder)
			found = true

			break
		}
	}

	if !found {
		lines = append(lines, fmt.Sprintf("RG_LATESTORDER=\"%d\"", latestOrder))
	}

	err = os.WriteFile(envFile, []byte(strings.Join(lines, "\n")), 0o600)
	if err != nil {
		return fmt.Errorf("ошибка записи файла: %w", err)
	}

	return nil
}

type PGConfig struct {
	PGHost     string
	PGPort     string
	PGUser     string
	PGPassword string
	PGDatabase string
}

func loadPGConfig() *PGConfig {
	pgHost := os.Getenv("PG_HOST")
	if pgHost == "" {
		panic("PG_HOST does not exist")
	}

	pgPort := os.Getenv("PG_PORT")
	if pgPort == "" {
		panic("PG_PORT does not exist")
	}

	pgUser := os.Getenv("PG_USER")
	if pgUser == "" {
		panic("PG_USER does not exist")
	}

	pgPassword := os.Getenv("PG_PASSWORD")
	if pgPassword == "" {
		panic("PG_PASSWORD does not exist")
	}

	pgDatabase := os.Getenv("PG_DATABASE")
	if pgDatabase == "" {
		panic("PG_DATABASE does not exist")
	}

	return &PGConfig{
		PGHost:     pgHost,
		PGPort:     pgPort,
		PGUser:     pgUser,
		PGPassword: pgPassword,
		PGDatabase: pgDatabase,
	}
}
