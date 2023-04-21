package handlers

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"example.com/Quaver/Z/config"
	"example.com/Quaver/Z/db"
	"example.com/Quaver/Z/utils"
	"fmt"
	"github.com/go-resty/resty/v2"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// LoginData The data that the user sends to log in
type LoginData struct {
	// The Steam ID of the user
	Id string `json:"id"`

	// Steam authentication PTicket
	PTicket string `json:"ticket"`

	// Steam authentication PCBTicket
	PcbTicket byte `json:"pcb"`

	// Game Client file signatures
	Client string `json:"client"`
}

// HandleLogin Handles the login of a client
func HandleLogin(conn net.Conn, r *http.Request) error {
	data, err := parseLoginData(r)

	if err != nil {
		return logFailedLogin(conn, err)
	}

	err = authenticateSteamTicket(data)

	if err != nil {
		return logFailedLogin(conn, err)
	}

	err = checkSteamAppOwnership(data.Id)

	if err != nil {
		return logFailedLogin(conn, err)
	}

	user, err := db.GetUserBySteamId(data.Id)

	if err != nil {
		if err == sql.ErrNoRows {
			// TODO: Send username selection packet
			log.Printf("[%v] %v logged in but does not have an account yet.\n", conn.RemoteAddr(), data.Id)
			utils.CloseConnection(conn)
			return nil
		}

		return err
	}

	if !user.Allowed {
		// TODO: Send notification packet
		log.Printf("[%v - #%v] Attempted to login, but they are banned\n", user.Username, user.Id)
		utils.CloseConnection(conn)
		return nil
	}

	err = verifyGameBuild(data)

	if err != nil {
		// User is logging in from an unknown client
		if err == sql.ErrNoRows {
			// TODO: Ban user if applicable (only devs, admins, and contributors are allowed to connect with modified clients)
			// TODO: Send anti-cheat webhook
			err = nil // suppress the error. No need to log it out up the chain.
		}

		// TODO: Send notification packet
		log.Printf("[%v - #%v] Attempted to login, but is using an invalid client", user.Username, user.Id)
		utils.CloseConnection(conn)
		return err
	}

	err = db.InsertLoginIpAddress(user.Id, conn.RemoteAddr().String())

	if err != nil {
		return err
	}

	err = db.UpdateUserLatestActivity(user.Id)

	if err != nil {
		return err
	}

	log.Println(user)
	return nil
}

// Parses the raw data into a LoginData struct
func parseLoginData(r *http.Request) (*LoginData, error) {
	data := r.URL.Query().Get("login")

	if data == "" {
		return nil, fmt.Errorf("empty login data")
	}

	decoded, err := base64.StdEncoding.DecodeString(data)

	if err != nil {
		return nil, fmt.Errorf("failed to decode login data - %v", err)
	}

	var parsed LoginData
	err = json.Unmarshal(decoded, &parsed)

	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal login data - %v", err)
	}

	return &parsed, nil
}

// Authenticates the user via Steam. Makes sure the user has a valid id and ticket
func authenticateSteamTicket(data *LoginData) error {
	resp, err := resty.New().R().
		SetQueryParams(map[string]string{
			"key":    config.Instance.Steam.PublisherKey,
			"appid":  strconv.Itoa(config.Instance.Steam.AppId),
			"ticket": strings.Replace(data.PTicket, "-", "", -1),
		}).
		Get("https://api.steampowered.com/ISteamUserAuth/AuthenticateUserTicket/v1/")

	if err != nil {
		return err
	}

	const failed string = "failed to authenticate steam ticket"

	if resp.IsError() {
		return fmt.Errorf("%v %v - %v", failed, resp.StatusCode(), string(resp.Body()))
	}

	type authenticateSteamTicketResponse struct {
		Response struct {
			Params struct {
				Result          string `json:"result,omitempty"`
				SteamId         string `json:"steamid,omitempty"`
				OwnerSteamId    string `json:"ownersteamid,omitempty"`
				VacBanned       bool   `json:"vacbanned,omitempty"`
				PublisherBanned bool   `json:"publisherbanned,omitempty"`
			} `json:"params"`

			Error interface{} `json:"error,omitempty"`
		} `json:"response"`
	}

	var parsed authenticateSteamTicketResponse
	err = json.Unmarshal(resp.Body(), &parsed)

	if err != nil {
		return fmt.Errorf("%v - json unmarshal - %v - %v", failed, err, string(resp.Body()))
	}

	if parsed.Response.Error != nil || parsed.Response.Params.Result != "OK" {
		return fmt.Errorf("%v - invalid response result - %v", failed, string(resp.Body()))
	}

	if parsed.Response.Params.VacBanned {
		return fmt.Errorf("%v - user is vac banned", failed)
	}

	if parsed.Response.Params.PublisherBanned {
		return fmt.Errorf("%v - user is publisher banned", failed)
	}

	if parsed.Response.Params.SteamId != data.Id {
		return fmt.Errorf("%v - response steam id does not match the user provided id (%v vs. %v)", failed, parsed.Response.Params.SteamId, data.Id)
	}

	return nil
}

// Checks if the user actually owns the game on Steam
func checkSteamAppOwnership(steamId string) error {
	resp, err := resty.New().R().
		SetQueryParams(map[string]string{
			"key":     config.Instance.Steam.PublisherKey,
			"appid":   strconv.Itoa(config.Instance.Steam.AppId),
			"steamid": steamId,
		}).
		Get("https://partner.steam-api.com/ISteamUser/CheckAppOwnership/v2/")

	if err != nil {
		return err
	}

	const failed string = "failed to check steam app ownership"

	if resp.IsError() {
		return fmt.Errorf("%v %v - %v", resp.StatusCode(), failed, string(resp.Body()))
	}

	type checkSteamAppOwnershipResponse struct {
		AppOwnership struct {
			OwnsApp bool `json:"ownsapp"`
		} `json:"appownership,omitempty"`

		Error interface{} `json:"error,omitempty"`
	}

	var parsed checkSteamAppOwnershipResponse
	err = json.Unmarshal(resp.Body(), &parsed)

	if err != nil {
		return fmt.Errorf("%v - json unmarshal - %v - %v", failed, err, string(resp.Body()))
	}

	if parsed.Error != nil {
		return fmt.Errorf("%v - %v", failed, string(resp.Body()))
	}

	if !parsed.AppOwnership.OwnsApp {
		return fmt.Errorf("%v - user does not own Quaver on Steam", failed)
	}

	return nil
}

// Checks the client signatures to see if the build they are using is valid
func verifyGameBuild(data *LoginData) error {
	split := strings.Split(data.Client, "|")

	if len(split) != 5 {
		return fmt.Errorf("user provided an incorrect amount of client signatures - %v", data.Client)
	}

	err := db.VerifyGameBuild(db.GameBuild{
		QuaverAPIDll:          split[1],
		QuaverServerClientDll: split[2],
		QuaverServerCommonDll: split[3],
		QuaverSharedDll:       split[4],
	})

	if err != nil {
		return err
	}

	return nil
}

func logFailedLogin(conn net.Conn, err error) error {
	return fmt.Errorf("[%v] login failed - %v", conn.RemoteAddr(), err)
}
