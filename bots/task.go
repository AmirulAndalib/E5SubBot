package bots

import (
	"fmt"
	"github.com/amirulandalib/E5SubBot/config"
	"github.com/amirulandalib/E5SubBot/model"
	"github.com/amirulandalib/E5SubBot/pkg/microsoft"
	"github.com/amirulandalib/E5SubBot/service/srv_client"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	tb "gopkg.in/tucnak/telebot.v2"
	"strconv"
	"time"
)

type ErrClient struct {
	*model.Client
	Err error
}

var (
	errorTimes  map[int]int
	signErr     map[int64]int
	unbindUsers []int64
	msgSender   *Sender
)

func InitTask() {
	errorTimes = make(map[int]int)
	msgSender = NewSender()

	c := cron.New()
	c.AddFunc(config.Cron, SignTask)
	c.Start()
}
func SignTask() {
	msgSender.Init(config.MaxGoroutines)

	signErr = make(map[int64]int)
	unbindUsers = nil

	clients := srv_client.GetAllClients()

	fmt.Printf("clients: %d goroutines:%d\n",
		len(clients),
		config.MaxGoroutines,
	)

	start := time.Now()

	errClients := Sign(clients)

	for _, errClient := range errClients {
		if errClient.Err != nil {
			opErrorSign(errClient)
			continue
		}
		// 请求一次成功清零errorTimes，避免接口的偶然错误积累导致账号被清退
		errorTimes[errClient.ID] = 0
		if err := srv_client.Update(errClient.Client); err != nil {
			zap.S().Errorw("failed to update")
		}
	}

	timeSpending := time.Since(start).Seconds()
	usersSummary(errClients)
	adminSummary(errClients, timeSpending)

	msgSender.Stop()
}

func adminSummary(errClients []*ErrClient, timeSpending float64) {
	var Count = len(errClients)
	var ErrCount int
	var ErrUserStr string
	var UnbindUserStr string
	for err, count := range signErr {
		ErrCount += count
		ErrUserStr += fmt.Sprintf("[%d](tg://user?id=%d)\n", err, err)
	}
	for _, unbindUser := range unbindUsers {
		UnbindUserStr += fmt.Sprintf("[%d](tg://user?id=%d)\n", unbindUser, unbindUser)
	}
	for _, admin := range config.Admins {
		a := admin
		msgSender.SendMessageByID(a, fmt.Sprintf("Task Feedback (Admin)\nCompletion Time: %s\nTime: %.2fs\nResult: %d/%d\nError Account: \n%s\nClear account: \n%s",
			time.Now().Format("2006-01-02 15:04:05"),
			timeSpending,
			Count-ErrCount, Count,
			ErrUserStr, UnbindUserStr,
		),
			tb.ModeMarkdown,
		)
	}
}
func usersSummary(errClients []*ErrClient) {

	var isSent map[int64]bool
	isSent = make(map[int64]bool)

	for _, errClient := range errClients {
		errClient := errClient
		// pending SignErrNum
		if errorTimes[errClient.ID] > config.MaxErrTimes {
			if err := srv_client.Del(errClient.ID); err != nil {
				zap.S().Errorw("failed to delete data",
					"error", err,
					"id", errClient.ID,
				)
				continue
			}

			unbindUsers = append(unbindUsers, errClient.TgId)

			msgSender.SendMessageByID(errClient.TgId, fmt.Sprintf("Your account was automatically unbound due to reaching the error limit\n\nAlias: %s\nclient_id: %s\nclient_secret: %s",
				errClient.Alias,
				errClient.ClientId,
				errClient.ClientSecret,
			))
			continue

		}
		if isSent[errClient.TgId] {
			continue
		}
		signOK := len(srv_client.GetClients(errClient.TgId)) - signErr[errClient.TgId]

		msgSender.SendMessageByID(errClient.TgId,
			fmt.Sprintf("Task feedback\nTime: %s\nResult:%d/%d",
				time.Now().Format("2006-01-02 15:04:05"),
				signOK,
				signErr[errClient.TgId]+signOK,
			),
		)
		isSent[errClient.TgId] = true
		time.Sleep(time.Millisecond * 100)
	}
}
func opErrorSign(errClient *ErrClient) {
	errorTimes[errClient.ID]++
	signErr[errClient.TgId]++

	UnBindBtn := tb.InlineButton{Unique: "un" + errClient.MsId, Text: "Click to unbind", Data: strconv.Itoa(errClient.ID)}
	bot.Handle(&UnBindBtn, bUnBindInlineBtn)

	msgSender.SendMessageByID(errClient.TgId,
		fmt.Sprintf("Sorry,,An error occurred while executing your account %s You can choose to unbind this user\nError: %s",
			errClient.Alias, errClient.Err),
		&tb.ReplyMarkup{InlineKeyboard: [][]tb.InlineButton{{UnBindBtn}}},
	)
}

func Sign(clients []*model.Client) []*ErrClient {
	var errClients []*ErrClient

	done := make(chan struct{})
	in := make(chan *ErrClient, 5)
	out := make(chan *ErrClient, 5)

	go func() {
		for _, client := range clients {
			in <- &ErrClient{
				Client: client,
				Err:    nil,
			}
		}
		close(in)
	}()
	for i := 0; i < config.MaxGoroutines; i++ {
		go func() {
			for {
				select {
				case errCli, f := <-in:
					if !f {
						continue
					}

					newRefresh, err := microsoft.GetOutlookMails(errCli.ClientId, errCli.ClientSecret, errCli.RefreshToken)
					errCli.Err = err
					errCli.RefreshToken = newRefresh
					out <- errCli
				case <-done:
					return
				}
			}
		}()
	}
	for i := 0; i < len(clients); i++ {
		errClient := <-out
		if errClient.Err == nil {
			fmt.Printf("%s OK\n", errClient.MsId)
		} else {
			zap.S().Errorw("failed to sign",
				"error", errClient.Err,
				"id", errClient.ID,
			)
			// fmt.Printf("%s %s\n",errClient.MsId,errClient.Err)
		}
		errClients = append(errClients, errClient)
	}
	close(done)
	return errClients
}