package main

import (
	"fmt"
	"log"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// 鉴权逻辑对应 includes/auth.php。
// 登录失败计数与锁定时间持久化在 settings 表中：
// 存会话的话，攻击者清空 Cookie 即可无限重置计数绕过锁定。

func isAdminLoggedIn(s *Session) bool {
	return s != nil && s.AdminLoggedIn
}

func passwordIsInitialized() bool {
	return dbCount("users", "1") > 0
}

type loginState struct {
	count       int
	lockedUntil int64
}

func loginAttemptState() loginState {
	return loginState{
		count:       dbFetchColumnInt("SELECT v FROM settings WHERE k = 'login_attempts'"),
		lockedUntil: int64(dbFetchColumnInt("SELECT v FROM settings WHERE k = 'login_locked_until'")),
	}
}

func assertLoginAllowed() error {
	st := loginAttemptState()
	if st.lockedUntil > time.Now().Unix() {
		waitMin := (st.lockedUntil - time.Now().Unix() + 59) / 60
		if waitMin < 1 {
			waitMin = 1
		}
		return fmt.Errorf("登录已临时锁定，请 %d 分钟后再试。", waitMin)
	}
	return nil
}

func saveSettingKv(key, value string) {
	existing := dbFetchOne("SELECT k FROM settings WHERE k = ?", key)
	if existing != nil {
		dbUpdate("settings", map[string]any{"v": value}, "k = ?", key)
	} else {
		dbInsert("settings", map[string]any{"k": key, "v": value})
	}
}

func markLoginFailure() {
	st := loginAttemptState()
	st.count++
	if st.count >= appConfig.LoginMaxAttempts {
		st.lockedUntil = time.Now().Unix() + int64(appConfig.LoginLockMinutes)*60
		st.count = 0
	}
	saveSettingKv("login_attempts", fmt.Sprintf("%d", st.count))
	saveSettingKv("login_locked_until", fmt.Sprintf("%d", st.lockedUntil))
}

func clearLoginFailures() {
	dbExecute("DELETE FROM settings WHERE k IN ('login_attempts', 'login_locked_until')")
}

func loginAdmin(username, password string) bool {
	if !passwordIsInitialized() {
		return false
	}
	user := dbFetchOne("SELECT * FROM users WHERE username = ?", username)
	if user == nil {
		// 与 PHP 版一致：用户不存在时也跑一次 bcrypt 比较，抹平时序差异
		_ = bcrypt.CompareHashAndPassword(
			[]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B0X0PYmVlSkC0pR0uW9Qy3jFaSKW"), []byte(password))
		return false
	}
	hash := str(user, "password_hash")
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return false
	}
	return true
}

func logoutAdmin(s *Session) {
	if s == nil {
		return
	}
	s.AdminLoggedIn = false
}

func setAdminPassword(username, password string) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("bcrypt: %v", err)
		return
	}
	existing := dbFetchOne("SELECT id FROM users LIMIT 1")
	if existing != nil {
		dbUpdate("users", map[string]any{
			"username":      username,
			"password_hash": string(hash),
		}, "id = ?", intVal(existing, "id"))
	} else {
		dbInsert("users", map[string]any{
			"username":      username,
			"password_hash": string(hash),
		})
	}
}
