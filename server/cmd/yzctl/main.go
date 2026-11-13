package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/platform/db"
)

const usage = "用法: yzctl <create-admin|reset-password> [参数]"

type action string

const (
	actionCreateAdmin   action = "create-admin"
	actionResetPassword action = "reset-password"
)

type command struct {
	action   action
	email    string
	name     string
	password string
}

type config struct {
	databaseURL string
	bcryptCost  int
}

func parseArgs(args []string, stdin io.Reader) (command, error) {
	if len(args) == 0 {
		return command{}, errors.New(usage)
	}
	act := action(args[0])
	if act != actionCreateAdmin && act != actionResetPassword {
		return command{}, fmt.Errorf("未知子命令 %q。%s", args[0], usage)
	}
	fs := flag.NewFlagSet(string(act), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	email := fs.String("email", "", "")
	password := fs.String("password", "", "")
	var name *string
	if act == actionCreateAdmin {
		name = fs.String("name", "", "")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return command{}, errors.New(subUsage(act))
		}
		return command{}, fmt.Errorf("参数错误: %w", err)
	}
	if fs.NArg() != 0 {
		return command{}, fmt.Errorf("存在多余参数。%s", subUsage(act))
	}
	cmd := command{
		action:   act,
		email:    strings.TrimSpace(*email),
		password: *password,
	}
	if cmd.email == "" {
		return command{}, fmt.Errorf("缺少 --email。%s", subUsage(act))
	}
	if name != nil {
		cmd.name = strings.TrimSpace(*name)
		if cmd.name == "" {
			return command{}, fmt.Errorf("缺少 --name。%s", subUsage(act))
		}
	}
	if cmd.password == "" {
		pass, err := readPasswordLine(stdin)
		if err != nil {
			return command{}, err
		}
		cmd.password = pass
	}
	return cmd, nil
}

func subUsage(act action) string {
	if act == actionCreateAdmin {
		return "用法: yzctl create-admin --email <邮箱> --name <姓名> [--password <密码>]"
	}
	return "用法: yzctl reset-password --email <邮箱> [--password <密码>]"
}

func readPasswordLine(r io.Reader) (string, error) {
	if r == nil {
		return "", errors.New("缺少标准输入，请通过 --password 或 stdin 提供密码")
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("读取密码失败: %w", err)
	}
	pass := strings.TrimRight(line, "\r\n")
	if pass == "" {
		return "", errors.New("密码不能为空")
	}
	return pass, nil
}

func loadConfig(getenv func(string) string) (config, error) {
	dbURL := strings.TrimSpace(getenv("DATABASE_URL"))
	if dbURL == "" {
		return config{}, errors.New("缺少环境变量 DATABASE_URL")
	}
	cost := bcrypt.DefaultCost
	if raw := strings.TrimSpace(getenv("BCRYPT_COST")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < bcrypt.MinCost || n > bcrypt.MaxCost {
			return config{}, fmt.Errorf("BCRYPT_COST 必须是 %d 到 %d 之间的整数", bcrypt.MinCost, bcrypt.MaxCost)
		}
		cost = n
	}
	return config{databaseURL: dbURL, bcryptCost: cost}, nil
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) error {
	cmd, err := parseArgs(args, stdin)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	pool, err := db.Connect(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	defer pool.Close()
	svc, err := identity.NewService(pool, nil, cfg.bcryptCost, clock.System{})
	if err != nil {
		return fmt.Errorf("初始化身份服务失败: %w", err)
	}
	switch cmd.action {
	case actionCreateAdmin:
		if err := svc.CreateAdmin(ctx, cmd.email, cmd.name, cmd.password); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "管理员已创建: %s\n", cmd.email)
		return nil
	case actionResetPassword:
		if err := svc.ResetPassword(ctx, cmd.email, cmd.password); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "密码已重置: %s\n", cmd.email)
		return nil
	default:
		return fmt.Errorf("未知子命令 %q", cmd.action)
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Getenv)
	cancel()
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "yzctl: %v\n", err)
		os.Exit(1)
	}
}
