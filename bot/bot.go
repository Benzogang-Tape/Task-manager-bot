package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	tgbotapi "github.com/skinass/telegram-bot-api/v5"
	"github.com/spf13/viper"
)

type BotMessages map[int64]string
type favContextKey string
type botCommandHandler func(context.Context, *Storage) (BotMessages, error)
type Users map[int64]*User
type Tasks []Task
type Commands map[string]botCommandHandler
type CommandTemplates []*regexp.Regexp

type Storage struct {
	botUsers Users
	tasks    Tasks
}

type User struct {
	ChatID   int64
	UserName string
}

type Task struct {
	TaskID       int64
	TaskValue    string
	TaskAuthor   *User
	TaskAssignee *User
}

const (
	availableActions = `/tasks - отобразить все задачи
/new <task> - создать новую задачу
/assign_<task_id> - назначить задачу на себя
/unassign_<task_id> - снять с себя задачу
/resolve_<task_id> - отметить задачу выполненной
/my - отобразить список моих задач
/owner - отобразить задачи, которые я создал`

	botMsgNoTasks           = "Нет задач"
	botMsgTaskNotFound      = "Задачи с таким id не найдено"
	botMsgYouAreNotAssignee = "Задача не на вас"
)

const (
	defaultPort    = "8081"
	portKey        = favContextKey("PORT")
	botTokenKey    = favContextKey("BOT_TOKEN")
	senderKey      = favContextKey("sender")
	idGeneratorKey = favContextKey("generateID")
	newTaskKey     = favContextKey("newTask")
	taskIDKey      = favContextKey("taskID")
	commandKey     = favContextKey("command")
)

var (
	BotToken   = ""
	WebhookURL = "http://127.0.0.1:8081"

	cmdTemplates = CommandTemplates{
		regexp.MustCompile(`^/start$`),
		regexp.MustCompile(`^/tasks$`),
		regexp.MustCompile(`^/new .*$`),
		regexp.MustCompile(`^/assign_[1-9]\d*$`),
		regexp.MustCompile(`^/unassign_[1-9]\d*$`),
		regexp.MustCompile(`^/resolve_[1-9]\d*$`),
		regexp.MustCompile(`^/my$`),
		regexp.MustCompile(`^/owner$`),
	}

	availableCommands = Commands{
		"/start":    startBotCmd,
		"/tasks":    listTasksBotCmd,
		"/new":      newTaskBotCmd,
		"/assign":   assignTaskBotCmd,
		"/unassign": unassignTaskBotCmd,
		"/resolve":  resolveTaskBotCmd,
		"/my":       myTasksBotCmd,
		"/owner":    tasksOwnerBotCmd,
	}
)

func idGenerator() func() int64 {
	var ID int64
	return func() int64 {
		atomic.AddInt64(&ID, 1)
		return ID
	}
}

func startBotCmd(ctx context.Context, _ *Storage) (BotMessages, error) {
	sender, ok := ctx.Value(senderKey).(User)
	if !ok {
		return nil, errors.New("startBotCmd: fail converting sender to a User type")
	}

	return BotMessages{
		sender.ChatID: availableActions,
	}, nil
}
func listTasksBotCmd(ctx context.Context, storage *Storage) (BotMessages, error) {
	sender, ok := ctx.Value(senderKey).(User)
	if !ok {
		return nil, errors.New("listTasksBotCmd: fail converting sender to a User type")
	}

	if len(storage.tasks) == 0 {
		return BotMessages{
			sender.ChatID: botMsgNoTasks,
		}, nil
	}

	botResponse := make([]string, len(storage.tasks))
	for taskIdx, task := range storage.tasks {
		botResponse[taskIdx] = fmt.Sprintf("%d. %s by @%s", task.TaskID, task.TaskValue, task.TaskAuthor.UserName)
		if task.TaskAssignee == nil {
			botResponse[taskIdx] += fmt.Sprintf("\n/assign_%d", task.TaskID)
			continue
		}

		botResponse[taskIdx] += "\nassignee: "
		if task.TaskAssignee.ChatID != sender.ChatID {
			botResponse[taskIdx] += "@" + task.TaskAssignee.UserName
			continue
		}

		botResponse[taskIdx] += fmt.Sprintf("я\n/unassign_%d /resolve_%d", task.TaskID, task.TaskID)
	}

	return BotMessages{
		sender.ChatID: strings.Join(botResponse, "\n\n"),
	}, nil
}

func newTaskBotCmd(ctx context.Context, storage *Storage) (BotMessages, error) {
	sender, ok := ctx.Value(senderKey).(User)
	if !ok {
		return nil, errors.New("newTaskBotCmd: fail converting sender to a User type")
	}

	generateID, ok := ctx.Value(idGeneratorKey).(func() int64)
	if !ok {
		return nil, errors.New("newTaskBotCmd: fail creating new task")
	}

	newTask, ok := ctx.Value(newTaskKey).(string)
	if !ok {
		return nil, errors.New("newTaskBotCmd: fail converting newTask to a string type")
	}

	taskID := generateID()
	storage.tasks = append(storage.tasks, Task{
		TaskID:       taskID,
		TaskValue:    newTask,
		TaskAuthor:   storage.botUsers[sender.ChatID],
		TaskAssignee: nil,
	})

	return BotMessages{
		sender.ChatID: fmt.Sprintf("Задача \"%s\" создана, id=%d", newTask, taskID),
	}, nil
}

func assignTaskBotCmd(ctx context.Context, storage *Storage) (BotMessages, error) {
	sender, ok := ctx.Value(senderKey).(User)
	if !ok {
		return nil, errors.New("assignTaskBotCmd: fail converting sender to a User type")
	}

	taskID, ok := ctx.Value(taskIDKey).(int64)
	if !ok {
		return nil, errors.New("assignTaskBotCmd: fail converting taskID to a int64 type")
	}

	taskIdx := slices.IndexFunc(storage.tasks, func(task Task) bool {
		return task.TaskID == taskID
	})
	if taskIdx == -1 {
		return BotMessages{
			sender.ChatID: botMsgTaskNotFound,
		}, nil
	}

	botResponse := make(BotMessages)
	if storage.tasks[taskIdx].TaskAssignee != nil {
		botResponse[storage.tasks[taskIdx].TaskAssignee.ChatID] = fmt.Sprintf("Задача \"%s\" назначена на @%s",
			storage.tasks[taskIdx].TaskValue, storage.botUsers[sender.ChatID].UserName)
	}

	if storage.tasks[taskIdx].TaskAssignee == nil && sender.ChatID != storage.tasks[taskIdx].TaskAuthor.ChatID {
		botResponse[storage.tasks[taskIdx].TaskAuthor.ChatID] = fmt.Sprintf("Задача \"%s\" назначена на @%s",
			storage.tasks[taskIdx].TaskValue, storage.botUsers[sender.ChatID].UserName)
	}

	botResponse[sender.ChatID] = fmt.Sprintf("Задача \"%s\" назначена на вас", storage.tasks[taskIdx].TaskValue)
	storage.tasks[taskIdx].TaskAssignee = storage.botUsers[sender.ChatID]
	return botResponse, nil
}

func unassignTaskBotCmd(ctx context.Context, storage *Storage) (BotMessages, error) {
	sender, ok := ctx.Value(senderKey).(User)
	if !ok {
		return nil, errors.New("unassignTaskBotCmd: fail converting sender to a User type")
	}

	taskID, ok := ctx.Value(taskIDKey).(int64)
	if !ok {
		return nil, errors.New("unassignTaskBotCmd: fail converting taskID to a int64 type")
	}

	taskIdx := slices.IndexFunc(storage.tasks, func(task Task) bool {
		return task.TaskID == taskID
	})
	if taskIdx == -1 {
		return BotMessages{
			sender.ChatID: botMsgTaskNotFound,
		}, nil
	}

	if storage.tasks[taskIdx].TaskAssignee == nil || sender.ChatID != storage.tasks[taskIdx].TaskAssignee.ChatID {
		return BotMessages{
			sender.ChatID: botMsgYouAreNotAssignee,
		}, nil
	}

	storage.tasks[taskIdx].TaskAssignee = nil
	return BotMessages{
		sender.ChatID:                            "Принято",
		storage.tasks[taskIdx].TaskAuthor.ChatID: fmt.Sprintf("Задача \"%s\" осталась без исполнителя", storage.tasks[taskIdx].TaskValue),
	}, nil
}

func resolveTaskBotCmd(ctx context.Context, storage *Storage) (BotMessages, error) {
	sender, ok := ctx.Value(senderKey).(User)
	if !ok {
		return nil, errors.New("resolveTaskBotCmd: fail converting sender to a User type")
	}

	taskID, ok := ctx.Value(taskIDKey).(int64)
	if !ok {
		return nil, errors.New("resolveTaskBotCmd: fail converting taskID to a int64 type")
	}

	taskIdx := slices.IndexFunc(storage.tasks, func(task Task) bool {
		return task.TaskID == taskID
	})
	if taskIdx == -1 {
		return BotMessages{
			sender.ChatID: botMsgTaskNotFound,
		}, nil
	}

	if storage.tasks[taskIdx].TaskAssignee == nil || sender.ChatID != storage.tasks[taskIdx].TaskAssignee.ChatID {
		return BotMessages{
			sender.ChatID: botMsgYouAreNotAssignee,
		}, nil
	}

	botMsgs := make(BotMessages)
	botMsgs[sender.ChatID] = fmt.Sprintf("Задача \"%s\" выполнена", storage.tasks[taskIdx].TaskValue)
	if sender.ChatID != storage.tasks[taskIdx].TaskAuthor.ChatID {
		botMsgs[storage.tasks[taskIdx].TaskAuthor.ChatID] = fmt.Sprintf("Задача \"%s\" выполнена @%s",
			storage.tasks[taskIdx].TaskValue, storage.tasks[taskIdx].TaskAssignee.UserName)
	}

	storage.tasks = slices.Delete(storage.tasks, taskIdx, taskIdx+1)
	return botMsgs, nil
}

func myTasksBotCmd(ctx context.Context, storage *Storage) (BotMessages, error) {
	sender, ok := ctx.Value(senderKey).(User)
	if !ok {
		return nil, errors.New("myTasksBotCmd: fail converting sender to a User type")
	}

	myTasks := make([]string, 0)
	for _, task := range storage.tasks {
		if task.TaskAssignee != nil && task.TaskAssignee.ChatID == sender.ChatID {
			myTasks = append(myTasks, fmt.Sprintf("%d. %s by @%s\n/unassign_%d /resolve_%d",
				task.TaskID, task.TaskValue, task.TaskAuthor.UserName, task.TaskID, task.TaskID))
		}
	}

	botResponse := make(BotMessages)
	if len(myTasks) == 0 {
		botResponse[sender.ChatID] = botMsgNoTasks
	} else {
		botResponse[sender.ChatID] = strings.Join(myTasks, "\n\n")
	}

	return botResponse, nil
}

func tasksOwnerBotCmd(ctx context.Context, storage *Storage) (BotMessages, error) {
	sender, ok := ctx.Value(senderKey).(User)
	if !ok {
		return nil, errors.New("tasksOwnerBotCmd: fail converting sender to a User type")
	}

	ownerTasks := make([]string, 0)
	for _, task := range storage.tasks {
		if task.TaskAuthor.ChatID == sender.ChatID {
			ownerTasks = append(ownerTasks, fmt.Sprintf("%d. %s by @%s", task.TaskID, task.TaskValue, task.TaskAuthor.UserName))
			if task.TaskAssignee == nil || task.TaskAssignee.ChatID != sender.ChatID {
				ownerTasks[len(ownerTasks)-1] += fmt.Sprintf("\n/assign_%d", task.TaskID)
				continue
			}
			ownerTasks[len(ownerTasks)-1] += fmt.Sprintf("\n/unassign_%d /resolve_%d", task.TaskID, task.TaskID)
		}
	}

	botResponse := make(BotMessages)
	if len(ownerTasks) == 0 {
		botResponse[sender.ChatID] = botMsgNoTasks
	} else {
		botResponse[sender.ChatID] = strings.Join(ownerTasks, "\n\n")
	}

	return botResponse, nil
}

func handleMessage(ctx context.Context, message *tgbotapi.Message, storage *Storage) (BotMessages, error) {
	if _, userExists := storage.botUsers[message.Chat.ID]; !userExists {
		storage.botUsers[message.Chat.ID] = &User{
			ChatID:   message.Chat.ID,
			UserName: message.From.UserName,
		}
	}

	var cmdMatch bool
	for _, cmdTmpl := range cmdTemplates {
		if cmdTmpl.MatchString(message.Text) {
			cmdMatch = true
			break
		}
	}

	if !cmdMatch {
		sender, ok := ctx.Value(senderKey).(User)
		if !ok {
			return nil, errors.New("handleMessage: fail converting sender to a User type")
		}
		return BotMessages{
			sender.ChatID: "Не удалось обработать команду",
		}, nil
	}

	cmd, suffix, found := strings.Cut(message.Text, "_")
	if found {
		taskID, err := strconv.Atoi(suffix)
		if err != nil {
			return nil, err
		}
		ctx = context.WithValue(ctx, taskIDKey, int64(taskID))
	} else {
		cmd, suffix, found = strings.Cut(message.Text, " ")
		if found {
			ctx = context.WithValue(ctx, newTaskKey, suffix)
		}
	}

	ctx = context.WithValue(ctx, commandKey, cmd)
	result, err := handleCommand(ctx, storage)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func handleCommand(ctx context.Context, storage *Storage) (BotMessages, error) {
	botCmd, ok := ctx.Value(commandKey).(string)
	if !ok {
		return nil, errors.New("handleCommand: fail converting command to a string type")
	}

	if _, cmdFound := availableCommands[botCmd]; !cmdFound {
		return nil, errors.New("handleCommand: no corresponding command was found")
	}

	result, err := availableCommands[botCmd](ctx, storage)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func startTaskBot(ctx context.Context) error {
	token := ctx.Value(botTokenKey).(string)
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return fmt.Errorf("startTaskBot: NewBotAPI failed: %s", err)
	}

	bot.Debug = true
	log.Printf("Authorized on account %s\n", bot.Self.UserName)

	// WITHOUT WEBHOOK (FOR DEMONSTRATION IN TELEGRAM)
	/*u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)*/

	// WITH WEBHOOK (FOR TESTS)
	wh, err := tgbotapi.NewWebhook(WebhookURL)
	if err != nil {
		log.Fatalf("NewWebhook failed: %s", err)
	}

	_, err = bot.Request(wh)
	if err != nil {
		log.Fatalf("SetWebhook failed: %s", err)
	}

	updates := bot.ListenForWebhook("/")

	port := ctx.Value(portKey).(string)
	go func() {
		log.Fatalln("http err:", http.ListenAndServe(":"+port, nil))
	}()
	fmt.Println("start listen :" + port)

	storage := Storage{
		botUsers: make(Users),
		tasks:    make(Tasks, 0),
	}
	generateID := idGenerator()
	ctx = context.WithValue(ctx, idGeneratorKey, generateID)
	for update := range updates {
		if update.Message == nil {
			continue
		}

		ctx = context.WithValue(ctx, senderKey, User{
			ChatID:   update.Message.Chat.ID,
			UserName: update.Message.Chat.UserName,
		})
		response, err := handleMessage(ctx, update.Message, &storage)
		if err != nil {
			return err
		}

		if err := sendResponse(bot, response); err != nil {
			return err
		}
	}
	return nil
}

func sendResponse(bot *tgbotapi.BotAPI, msgs BotMessages) error {
	for chatID, message := range msgs {
		msg := tgbotapi.NewMessage(chatID, message)
		_, err := bot.Send(msg)
		if err != nil {
			return err
		}
	}
	return nil
}

func main() {
	initConfig()
	token := viper.GetString("bot.token")
	if token == "" {
		panic(errors.New("bot token is not set"))
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, botTokenKey, token)
	ctx = context.WithValue(ctx, portKey, defaultPort)
	if viper.IsSet("app.port") {
		ctx = context.WithValue(ctx, portKey, viper.GetString("app.port"))
	}
	err := startTaskBot(ctx)
	if err != nil {
		panic(err)
	}
}

func initConfig() {
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	if err := viper.ReadInConfig(); err != nil {
		panic(fmt.Errorf("fatal error config file: %w", err))
	}
}
