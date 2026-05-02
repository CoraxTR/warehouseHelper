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
	*AppConfig
	*MoySkladConfig
	*RefGoConfig
	*PGConfig
}

func NewConfig() *Config {
	err := godotenv.Load("../.env")
	if err != nil {
		panic("Cannot read config file")
	}

	apc := loadAppconfig()
	msc := loadMoySkladConfig()
	rfc := loadRefGoConfig()
	pfc := loadPGConfig()

	if os.Getenv("RG_LATESTORDER") == "" {
		panic("RG_LATESTORDER does not exist")
	}

	return &Config{
		AppConfig:      apc,
		MoySkladConfig: msc,
		RefGoConfig:    rfc,
		PGConfig:       pfc,
	}
}

type AppConfig struct {
	HTTPAddress string
}

func loadAppconfig() *AppConfig {
	httpAddress := os.Getenv("APP_HTTPADDRESS")
	if httpAddress == "" {
		os.Exit(1)
	}

	return &AppConfig{
		HTTPAddress: httpAddress,
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
		os.Exit(1)
	}

	tspnint, err := strconv.Atoi(os.Getenv("MSAPI_REQUESTCAPTIMESPAN"))
	if err != nil {
		os.Exit(1)
	}

	tspn := time.Duration(int64(tspnint)) * time.Second

	rqcap, err := strconv.Atoi(os.Getenv("MSAPI_REQUESTCAP"))
	if err != nil {
		os.Exit(1)
	}

	selltypeID := os.Getenv("MSAPI_SELLTYPEID")
	if selltypeID == "" {
		os.Exit(1)
	}

	refgonumberid := os.Getenv("MSAPI_REFGONUMBERID")
	if refgonumberid == "" {
		os.Exit(1)
	}

	courierid := os.Getenv("MSAPI_COURIERID")
	if courierid == "" {
		os.Exit(1)
	}

	timeFormat := os.Getenv("MSAPI_TIMEFORMAT")
	if timeFormat == "" {
		os.Exit(1)
	}

	urlstart := os.Getenv("MSAPI_URLSTART")
	if urlstart == "" {
		os.Exit(1)
	}

	authheader := os.Getenv("MSAPI_AUTHHEADER")
	if authheader == "" {
		os.Exit(1)
	}

	encodeheader := os.Getenv("MSAPI_ENCODEHEADER")
	if encodeheader == "" {
		os.Exit(1)
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
	Printtemplatehref string
}

func loadMoySkladhrefs() *MoySkladhrefs {
	readystatehref := os.Getenv("MSAPI_READYSTATEHREF")
	if readystatehref == "" {
		os.Exit(1)
	}

	shipedstatehref := os.Getenv("MSAPI_SHIPEDSTATEHREF")
	if shipedstatehref == "" {
		os.Exit(1)
	}

	selltypehref := os.Getenv("MSAPI_SELLTYPEHREF")
	if selltypehref == "" {
		os.Exit(1)
	}

	selltypeOtherhref := os.Getenv("MSAPI_SELLTYPEOTHERHREF")
	if selltypeOtherhref == "" {
		os.Exit(1)
	}

	storehref := os.Getenv("MSAPI_STOREHREF")
	if storehref == "" {
		os.Exit(1)
	}

	orghref := os.Getenv("MSAPI_ORGHREF")
	if orghref == "" {
		os.Exit(1)
	}

	refgonumberhref := os.Getenv("MSAPI_REFGONUMBERHREF")
	if refgonumberhref == "" {
		os.Exit(1)
	}

	courierhref := os.Getenv("MSAPI_COURIERHREF")
	if courierhref == "" {
		os.Exit(1)
	}

	refgocourierhref := os.Getenv("MSAPI_REFGOCOURIERHREF")
	if refgocourierhref == "" {
		os.Exit(1)
	}

	printtemplatehref := os.Getenv("MSAPI_PRINTTEMPLATEHREF")

	if printtemplatehref == "" {
		os.Exit(1)
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
		Printtemplatehref: printtemplatehref,
	}
}

type RefGoConfig struct {
	RGNextOrder int
}

func loadRefGoConfig() *RefGoConfig {
	if os.Getenv("RG_LATESTORDER") == "" {
		os.Exit(1)
	}

	latestorder, err := strconv.Atoi(strings.Trim(os.Getenv("RG_LATESTORDER"), `"`))
	if err != nil {
		os.Exit(1)
	}

	return &RefGoConfig{
		RGNextOrder: latestorder,
	}
}

func (rgc *RefGoConfig) ChangeRefGoLatest(latestOrder int) error {
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

	rgc.RGNextOrder = latestOrder

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
		os.Exit(1)
	}

	pgPort := os.Getenv("PG_PORT")
	if pgPort == "" {
		os.Exit(1)
	}

	pgUser := os.Getenv("PG_USER")
	if pgUser == "" {
		os.Exit(1)
	}

	pgPassword := os.Getenv("PG_PASSWORD")
	if pgPassword == "" {
		os.Exit(1)
	}

	pgDatabase := os.Getenv("PG_DATABASE")
	if pgDatabase == "" {
		os.Exit(1)
	}

	return &PGConfig{
		PGHost:     pgHost,
		PGPort:     pgPort,
		PGUser:     pgUser,
		PGPassword: pgPassword,
		PGDatabase: pgDatabase,
	}
}
