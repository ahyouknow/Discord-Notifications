package main

import (
	"fmt"
	"os"
	"os/signal"
	"bufio"
	"syscall"
	"image/png"
	"io/ioutil"
	"strings"
	"encoding/json"

	"golang.org/x/crypto/ssh/terminal"
	"github.com/mqu/go-notify"
	"github.com/bwmarrin/discordgo"
)

// Variables for categorizing guilds and channels
var (
	suppressEveryone []string
	mentionsOnly []string
	noNotifications []string
)

// Configuration structure
type Config struct {
	Token string
}

// Default configuration
var config *Config = &Config{
	Token: "",
}

func main() {
	part, err := getConfig()
	if err != nil {
		fmt.Println(part)
		fmt.Println(err)
		return
	}
	// initalize notifications
	notify.Init("Discord")

	// Create a new Discord session using the provided bot token.
	dg, err := discordgo.New(config.Token)
	if err != nil {
		fmt.Println("error creating Discord session,", err)
		return
	}

	dg.AddHandler(messageCreate)
	dg.AddHandler(ready)
	dg.AddHandler(UserGuildSettingsUpdate)

	// Open a websocket connection to Discord and begin listening.
	err = dg.Open()
	if err != nil {
		fmt.Println("error opening connection,", err)
		return
	}

	// Wait here until CTRL-C or other term signal is received.
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-sc

	// Cleanly close down the Discord session.
	dg.Close()
}

func getConfig() (string, error) {
	// get Config file
	// if Config file or folder does not exist create it and ask for user login
	homedir, err := os.UserHomeDir()
	if err != nil {
		return "Getting Home Directory", err
	}

	// checks if config directory exists 
	// if it does not exist create it with read and write permissions for user and group
	configDir := homedir+".config/discordnotify/"
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		err := os.Mkdir(configDir, 0774)
		if err != nil {
			return "Error in making configuration Directory", err
		}
	}

	// Checks if config file exists if it does not then write the default config
	configFilePath := configDir+"config.json"
	if _, err := os.Stat(configFilePath); os.IsNotExist(err) {
		writeConfig(configFilePath)
	}

	// retrives the config from the file
	configFile, err := os.Open(configFilePath)
	if err != nil{
		return "Error in opening config file", err
	}
	decoder := json.NewDecoder(configFile)
	err = decoder.Decode(config)
	configFile.Close()
	if err != nil {
		return "Error in decoding config file", err
	}

	// If there is no token in the config file then have the user log in
	// and write the token in the config
	if (config.Token == "") {
		email, password, err := credentials()
		if err != nil {
			return "error with getting email and password", err
		}
		dg, err := discordgo.New(email, password)
		if err != nil {
			return "Error in logging into Discord", err
		}
		config.Token = dg.Token
		dg.Close()
		err = writeConfig(configFilePath)
		if err != nil {
			return "Error writing config file", err
		}
	}
	return "", nil
}

func writeConfig(configFilePath string) (error) {
	configAsJSON, err  := json.Marshal(config)
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(configFilePath, configAsJSON, 0666)
	if err != nil {
		return err
	}
	return nil
}

// gets email and password
func credentials() (string, string, error) {
    reader := bufio.NewReader(os.Stdin)

    fmt.Print("Enter Email: ")
    email, _ := reader.ReadString('\n')

    fmt.Print("Enter Password: ")
    bytePassword, err := terminal.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return "", "", err
	}
    password := string(bytePassword)
	fmt.Println("")

    return strings.TrimSpace(email), strings.TrimSpace(password), nil
}

// When bot is ready it will get all notification settings for guilds and channels
func ready(s *discordgo.Session, e *discordgo.Ready) {
	for _, guild := range e.Guilds {
		for index, guildSettings := range e.UserGuildSettings {
			if (guild.ID == guildSettings.GuildID){
				sortGuildNotifications(guildSettings)
				for _, channelSettings := range guildSettings.ChannelOverrides {
					sortChannelNotifications(channelSettings)
				}
				break
			}

			// If the guild id is not in the User Guild Settings then it has default message notifications
			// The guild will also not show up if none of the channels have been muted
			if (index == len(e.UserGuildSettings)-1) && (guild.DefaultMessageNotifications == 1){
				mentionsOnly = append(mentionsOnly, guild.ID)
		}	}
	}
}

// Categorizes which guild has which notification settings
func sortGuildNotifications(settings *discordgo.UserGuildSettings) {
	// Muted Guilds function the same as guilds with mentions only
	if settings.Muted || (settings.MessageNotifications == 1){
		mentionsOnly = append(mentionsOnly, settings.GuildID)
	} else if (settings.MessageNotifications == 2) {
		noNotifications = append(noNotifications, settings.GuildID)
	}
	if settings.SupressEveryone {
		suppressEveryone = append(suppressEveryone, settings.GuildID)
	}
}

// Categroizes which channels have which notification settings
func sortChannelNotifications(channel *discordgo.UserGuildSettingsChannelOverride) {
	// Muted Channels function the same as the nothing setting
	if channel.Muted || (channel.MessageNotifications == 2) {
		noNotifications = append(noNotifications, channel.ChannelID)
	} else if (channel.MessageNotifications == 1) {
		mentionsOnly = append(mentionsOnly, channel.ChannelID)
	}
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}

	guildID := m.GuildID
	channelID := m.ChannelID

	// Check if server or channel is blocking all notifications
	if multipleCheckIn(guildID, channelID, noNotifications) {
		return
	}

	// Check if server is suppressing @everyone and @here
	if m.MentionEveryone {
		for _, id := range suppressEveryone {
			if id == guildID {
				return
			}
		sendNotification(s, guildID)
		return
		}
	}

	// Check if channel or server is in mentions only and sends the notification if the user is mentioned
	if multipleCheckIn(guildID, channelID, mentionsOnly){
		for _, user := range m.Mentions {
			if (user.ID == s.State.User.ID) {
				sendNotification(s, guildID)
				return
			}
		}
		return
	}

	sendNotification(s, guildID)
}

func sendNotification(s *discordgo.Session, guildID string) {
	// Gets the guild icon and puts it in a temp file
	// If any error occurs it will still send the notification without the icon
	guildIcon, err := s.GuildIcon(guildID)
	var tmpFilePath string
	if err != nil {
		fmt.Println(err)
	} else {
		tmpFile, err := ioutil.TempFile("/tmp", "image")
		if err != nil {
			fmt.Println(err)
		} else {
			png.Encode(tmpFile, guildIcon)
			tmpFilePath = tmpFile.Name()
			defer os.Remove(tmpFilePath)
		}
	}

	// Create and send the notification then delete the temp file if there were no errors
	hello := notify.NotificationNew("Discord", "New message", tmpFilePath)
	hello.Show()
}

func UserGuildSettingsUpdate(s *discordgo.Session, settingsUpdate *discordgo.UserGuildSettingsUpdate) {
	if removeFrom(settingsUpdate.GuildID, suppressEveryone) {
		suppressEveryone = suppressEveryone[:len(suppressEveryone)-1]
	}
	// Check first if server or channel was muted then check the other notification
	// After removing old settings apply new settings
	if removeFrom(settingsUpdate.GuildID, mentionsOnly) {
		mentionsOnly = mentionsOnly[:len(mentionsOnly)-1]
	} else if removeFrom(settingsUpdate.GuildID, noNotifications) {
		noNotifications = noNotifications[:len(noNotifications)-1]
	}
	sortGuildNotifications(settingsUpdate.UserGuildSettings)
	for _, channelSettings := range settingsUpdate.ChannelOverrides {
		if removeFrom(channelSettings.ChannelID, noNotifications) {
			noNotifications = noNotifications[:len(noNotifications)-1]
		} else if removeFrom(channelSettings.ChannelID, mentionsOnly) {
			mentionsOnly = mentionsOnly[:len(mentionsOnly)-1]
		}
		sortChannelNotifications(channelSettings)
	}
}

// checks if an ID is present in array
func checkIn(id string, ids []string) (bool, int) {
	for index, checkid := range ids {
		if checkid == id {
			return true, index
		}
	}
	return false, len(ids)
}

// Checks if either ID is in the array
func multipleCheckIn(idOne, idTwo string, ids []string) (bool) {
	for _, checkid := range ids {
		if (checkid == idOne) || (checkid == idTwo) {
			return true
		}
	}
	return false
}

// Removes id from array returns true if element was in array false if not
func removeFrom(id string, ids []string) (bool) {
	boolean, index := checkIn(id, ids)
	if boolean {
		ids[index] = ids[len(ids)-1]
		ids[len(ids)-1] = ""
		return true
	} else {
		return false
	}
}

