package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/services"
)

func runUserCommand(args []string) error {
	if len(args) == 0 {
		printUserUsage()
		return flag.ErrHelp
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("user list 不接受额外参数")
		}
		return listUsers()
	case "reset-password":
		return resetUserPassword(args[1:])
	case "help", "-h", "--help":
		printUserUsage()
		return nil
	default:
		return fmt.Errorf("未知的用户应急命令 %q", args[0])
	}
}

func listUsers() error {
	users, closeDB, err := emergencyUserService()
	if err != nil {
		return err
	}
	defer closeDB()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	items, err := users.List(services.WithWriter(ctx), "")
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tUSERNAME\tDISPLAY NAME\tROLE\tEMAIL")
	for _, user := range items {
		fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\n", user.UID, user.Name, user.ScreenName, user.Role, user.Mail)
	}
	return writer.Flush()
}

func resetUserPassword(args []string) error {
	fs := flag.NewFlagSet("gopherink user reset-password", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	id := fs.Int64("id", 0, "目标用户 ID")
	name := fs.String("name", "", "目标用户名")
	password := fs.String("password", "", "新密码（会保留在 Shell 历史中，建议使用交互输入）")
	passwordStdin := fs.Bool("password-stdin", false, "从标准输入第一行读取新密码")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("无法识别的参数: %s", strings.Join(fs.Args(), " "))
	}
	if (*id > 0) == (strings.TrimSpace(*name) != "") {
		return errors.New("必须且只能指定 --id 或 --name 其中之一")
	}
	if *password != "" && *passwordStdin {
		return errors.New("--password 和 --password-stdin 不能同时使用")
	}
	if *passwordStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		*password = strings.TrimRight(line, "\r\n")
	} else if *password == "" {
		if !stdinIsTerminal() {
			return errors.New("非交互环境请使用 --password-stdin 提供新密码")
		}
		first, err := promptSecret(os.Stdout, "新密码")
		if err != nil {
			return err
		}
		second, err := promptSecret(os.Stdout, "再次输入新密码")
		if err != nil {
			return err
		}
		if first != second {
			return errors.New("两次输入的密码不一致")
		}
		*password = first
	}
	if utf8.RuneCountInString(*password) < 6 {
		return errors.New("密码至少需要 6 个字符")
	}

	users, closeDB, err := emergencyUserService()
	if err != nil {
		return err
	}
	defer closeDB()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ctx = services.WithWriter(ctx)
	var user models.User
	if *id > 0 {
		user, err = users.ByID(ctx, *id)
	} else {
		user, err = users.ByName(ctx, strings.TrimSpace(*name))
	}
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	if err := users.ChangePassword(ctx, user.UID, *password); err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	fmt.Printf("用户 %s（ID %s）的密码已重置，现有登录会话已失效。\n", user.Name, strconv.FormatInt(user.UID, 10))
	return nil
}

func emergencyUserService() (*services.UserService, func(), error) {
	cfg, err := loadConfig(nil, false)
	if err != nil {
		return nil, nil, err
	}
	db, err := openDB(cfg)
	if err != nil {
		return nil, nil, err
	}
	serviceDB := services.DB(services.NewSQLDB(db, cfg.DBDriver))
	return services.NewUserService(serviceDB), func() { _ = db.Close() }, nil
}

func printUserUsage() {
	fmt.Println(`用户应急命令:
  gopherink user list
  gopherink user reset-password --id ID
  gopherink user reset-password --name USERNAME
  printf 'new-password\n' | gopherink user reset-password --id ID --password-stdin`)
}
