// Package pam provides a wrapper for the PAM application API.
package pam

//#cgo CFLAGS: -Wall -std=c99
//#cgo LDFLAGS: -lpam
//
//#include <security/pam_appl.h>
//#include <stdlib.h>
//#include <stdint.h>
//
//#ifdef PAM_BINARY_PROMPT
//#define BINARY_PROMPT_IS_SUPPORTED 1
//#else
//#include <limits.h>
//#define PAM_BINARY_PROMPT INT_MAX
//#define BINARY_PROMPT_IS_SUPPORTED 0
//#endif
//
//void init_pam_conv(struct pam_conv *conv, uintptr_t);
//int pam_start_confdir(const char *service_name, const char *user, const struct pam_conv *pam_conversation, const char *confdir, pam_handle_t **pamh) __attribute__ ((weak));
//int check_pam_start_confdir(void);
import "C"

import (
	"errors"
	"runtime"
	"runtime/cgo"
	"strings"
	"unsafe"
)

// Style is the type of message that the conversation handler should display.
type Style int

// Coversation handler style types.
const (
	// PromptEchoOff indicates the conversation handler should obtain a
	// string without echoing any text.
	PromptEchoOff Style = C.PAM_PROMPT_ECHO_OFF
	// PromptEchoOn indicates the conversation handler should obtain a
	// string while echoing text.
	PromptEchoOn = C.PAM_PROMPT_ECHO_ON
	// ErrorMsg indicates the conversation handler should display an
	// error message.
	ErrorMsg = C.PAM_ERROR_MSG
	// TextInfo indicates the conversation handler should display some
	// text.
	TextInfo = C.PAM_TEXT_INFO
	// BinaryPrompt indicates the conversation handler that should implement
	// the private binary protocol
	BinaryPrompt = C.PAM_BINARY_PROMPT
)

// ConversationHandler is an interface for objects that can be used as
// conversation callbacks during PAM authentication.
type ConversationHandler interface {
	// RespondPAM receives a message style and a message string. If the
	// message Style is PromptEchoOff or PromptEchoOn then the function
	// should return a response string.
	RespondPAM(Style, string) (string, error)
}

// BinaryPointer exposes the type used for the data in a binary conversation
// it represents a pointer to data that is produced by the module and that
// must be parsed depending on the protocol in use
type BinaryPointer unsafe.Pointer

// BinaryConversationHandler is an interface for objects that can be used as
// conversation callbacks during PAM authentication if binary protocol is going
// to be supported.
type BinaryConversationHandler interface {
	ConversationHandler
	// RespondPAMBinary receives a pointer to the binary message. It's up to
	// the receiver to parse it according to the protocol specifications.
	// The function can return a byte array that will passed as pointer back
	// to the module.
	RespondPAMBinary(BinaryPointer) ([]byte, error)
}

// ConversationFunc is an adapter to allow the use of ordinary functions as
// conversation callbacks.
type ConversationFunc func(Style, string) (string, error)

// RespondPAM is a conversation callback adapter.
func (f ConversationFunc) RespondPAM(s Style, msg string) (string, error) {
	return f(s, msg)
}

// cbPAMConv is a wrapper for the conversation callback function.
//
//export cbPAMConv
func cbPAMConv(s C.int, msg *C.char, c C.uintptr_t) (*C.char, C.int) {
	var r string
	var err error
	v := cgo.Handle(c).Value()
	style := Style(s)
	switch cb := v.(type) {
	case BinaryConversationHandler:
		if style == BinaryPrompt {
			bytes, err := cb.RespondPAMBinary(BinaryPointer(msg))
			if err != nil {
				return nil, C.PAM_CONV_ERR
			}
			return (*C.char)(C.CBytes(bytes)), C.PAM_SUCCESS
		} else {
			r, err = cb.RespondPAM(style, C.GoString(msg))
		}
	case ConversationHandler:
		if style == BinaryPrompt {
			return nil, C.PAM_AUTHINFO_UNAVAIL
		}
		r, err = cb.RespondPAM(style, C.GoString(msg))
	}
	if err != nil {
		return nil, C.PAM_CONV_ERR
	}
	return C.CString(r), C.PAM_SUCCESS
}

// Transaction is the application's handle for a PAM transaction.
type Transaction struct {
	handle *C.pam_handle_t
	conv   *C.struct_pam_conv
	status C.int
	c      cgo.Handle
}

// transactionFinalizer cleans up the PAM handle and deletes the callback
// function.
func transactionFinalizer(t *Transaction) {
	C.pam_end(t.handle, t.status)
	t.c.Delete()
}

// Start initiates a new PAM transaction. Service is treated identically to
// how pam_start treats it internally.
//
// All application calls to PAM begin with Start*. The returned
// transaction provides an interface to the remainder of the API.
func Start(service, user string, handler ConversationHandler) (*Transaction, error) {
	return start(service, user, handler, "")
}

// StartFunc registers the handler func as a conversation handler.
func StartFunc(service, user string, handler func(Style, string) (string, error)) (*Transaction, error) {
	return Start(service, user, ConversationFunc(handler))
}

// StartConfDir initiates a new PAM transaction. Service is treated identically to
// how pam_start treats it internally.
// confdir allows to define where all pam services are defined. This is used to provide
// custom paths for tests.
//
// All application calls to PAM begin with Start*. The returned
// transaction provides an interface to the remainder of the API.
func StartConfDir(service, user string, handler ConversationHandler, confDir string) (*Transaction, error) {
	if !CheckPamHasStartConfdir() {
		return nil, errors.New("StartConfDir() was used, but the pam version on the system is not recent enough")
	}

	return start(service, user, handler, confDir)
}

func start(service, user string, handler ConversationHandler, confDir string) (*Transaction, error) {
	switch handler.(type) {
	case BinaryConversationHandler:
		if !CheckPamHasBinaryProtocol() {
			return nil, errors.New("BinaryConversationHandler() was used, but it is not supported by this platform")
		}
	}
	t := &Transaction{
		conv: &C.struct_pam_conv{},
		c:    cgo.NewHandle(handler),
	}
	C.init_pam_conv(t.conv, C.uintptr_t(t.c))
	runtime.SetFinalizer(t, transactionFinalizer)
	s := C.CString(service)
	defer C.free(unsafe.Pointer(s))
	var u *C.char
	if len(user) != 0 {
		u = C.CString(user)
		defer C.free(unsafe.Pointer(u))
	}
	if confDir == "" {
		t.status = C.pam_start(s, u, t.conv, &t.handle)
	} else {
		c := C.CString(confDir)
		defer C.free(unsafe.Pointer(c))
		t.status = C.pam_start_confdir(s, u, t.conv, c, &t.handle)
	}
	if t.status != C.PAM_SUCCESS {
		return nil, t
	}
	return t, nil
}

func (t *Transaction) Error() string {
	return C.GoString(C.pam_strerror(t.handle, C.int(t.status)))
}

// Item is a an PAM information type.
type Item int

// PAM Item types.
const (
	// Service is the name which identifies the PAM stack.
	Service Item = C.PAM_SERVICE
	// User identifies the username identity used by a service.
	User = C.PAM_USER
	// Tty is the terminal name.
	Tty = C.PAM_TTY
	// Rhost is the requesting host name.
	Rhost = C.PAM_RHOST
	// Authtok is the currently active authentication token.
	Authtok = C.PAM_AUTHTOK
	// Oldauthtok is the old authentication token.
	Oldauthtok = C.PAM_OLDAUTHTOK
	// Ruser is the requesting user name.
	Ruser = C.PAM_RUSER
	// UserPrompt is the string use to prompt for a username.
	UserPrompt = C.PAM_USER_PROMPT
)

// SetItem sets a PAM information item.
func (t *Transaction) SetItem(i Item, item string) error {
	cs := unsafe.Pointer(C.CString(item))
	defer C.free(cs)
	t.status = C.pam_set_item(t.handle, C.int(i), cs)
	if t.status != C.PAM_SUCCESS {
		return t
	}
	return nil
}

// GetItem retrieves a PAM information item.
func (t *Transaction) GetItem(i Item) (string, error) {
	var s unsafe.Pointer
	t.status = C.pam_get_item(t.handle, C.int(i), &s)
	if t.status != C.PAM_SUCCESS {
		return "", t
	}
	return C.GoString((*C.char)(s)), nil
}

// Flags are inputs to various PAM functions than be combined with a bitwise
// or. Refer to the official PAM documentation for which flags are accepted
// by which functions.
type Flags int

// PAM Flag types.
const (
	// Silent indicates that no messages should be emitted.
	Silent Flags = C.PAM_SILENT
	// DisallowNullAuthtok indicates that authorization should fail
	// if the user does not have a registered authentication token.
	DisallowNullAuthtok = C.PAM_DISALLOW_NULL_AUTHTOK
	// EstablishCred indicates that credentials should be established
	// for the user.
	EstablishCred = C.PAM_ESTABLISH_CRED
	// DeleteCred inidicates that credentials should be deleted.
	DeleteCred = C.PAM_DELETE_CRED
	// ReinitializeCred indicates that credentials should be fully
	// reinitialized.
	ReinitializeCred = C.PAM_REINITIALIZE_CRED
	// RefreshCred indicates that the lifetime of existing credentials
	// should be extended.
	RefreshCred = C.PAM_REFRESH_CRED
	// ChangeExpiredAuthtok indicates that the authentication token
	// should be changed if it has expired.
	ChangeExpiredAuthtok = C.PAM_CHANGE_EXPIRED_AUTHTOK
)

// Authenticate is used to authenticate the user.
//
// Valid flags: Silent, DisallowNullAuthtok
func (t *Transaction) Authenticate(f Flags) error {
	t.status = C.pam_authenticate(t.handle, C.int(f))
	if t.status != C.PAM_SUCCESS {
		return t
	}
	return nil
}

// SetCred is used to establish, maintain and delete the credentials of a
// user.
//
// Valid flags: EstablishCred, DeleteCred, ReinitializeCred, RefreshCred
func (t *Transaction) SetCred(f Flags) error {
	t.status = C.pam_setcred(t.handle, C.int(f))
	if t.status != C.PAM_SUCCESS {
		return t
	}
	return nil
}

// AcctMgmt is used to determine if the user's account is valid.
//
// Valid flags: Silent, DisallowNullAuthtok
func (t *Transaction) AcctMgmt(f Flags) error {
	t.status = C.pam_acct_mgmt(t.handle, C.int(f))
	if t.status != C.PAM_SUCCESS {
		return t
	}
	return nil
}

// ChangeAuthTok is used to change the authentication token.
//
// Valid flags: Silent, ChangeExpiredAuthtok
func (t *Transaction) ChangeAuthTok(f Flags) error {
	t.status = C.pam_chauthtok(t.handle, C.int(f))
	if t.status != C.PAM_SUCCESS {
		return t
	}
	return nil
}

// OpenSession sets up a user session for an authenticated user.
//
// Valid flags: Slient
func (t *Transaction) OpenSession(f Flags) error {
	t.status = C.pam_open_session(t.handle, C.int(f))
	if t.status != C.PAM_SUCCESS {
		return t
	}
	return nil
}

// CloseSession closes a previously opened session.
//
// Valid flags: Silent
func (t *Transaction) CloseSession(f Flags) error {
	t.status = C.pam_close_session(t.handle, C.int(f))
	if t.status != C.PAM_SUCCESS {
		return t
	}
	return nil
}

// PutEnv adds or changes the value of PAM environment variables.
//
// NAME=value will set a variable to a value.
// NAME= will set a variable to an empty value.
// NAME (without an "=") will delete a variable.
func (t *Transaction) PutEnv(nameval string) error {
	cs := C.CString(nameval)
	defer C.free(unsafe.Pointer(cs))
	t.status = C.pam_putenv(t.handle, cs)
	if t.status != C.PAM_SUCCESS {
		return t
	}
	return nil
}

// GetEnv is used to retrieve a PAM environment variable.
func (t *Transaction) GetEnv(name string) string {
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	value := C.pam_getenv(t.handle, cs)
	if value == nil {
		return ""
	}
	return C.GoString(value)
}

func next(p **C.char) **C.char {
	return (**C.char)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + unsafe.Sizeof(p)))
}

// GetEnvList returns a copy of the PAM environment as a map.
func (t *Transaction) GetEnvList() (map[string]string, error) {
	env := make(map[string]string)
	p := C.pam_getenvlist(t.handle)
	if p == nil {
		t.status = C.PAM_BUF_ERR
		return nil, t
	}
	for q := p; *q != nil; q = next(q) {
		chunks := strings.SplitN(C.GoString(*q), "=", 2)
		if len(chunks) == 2 {
			env[chunks[0]] = chunks[1]
		}
		C.free(unsafe.Pointer(*q))
	}
	C.free(unsafe.Pointer(p))
	return env, nil
}

// CheckPamHasStartConfdir return if pam on system supports pam_system_confdir
func CheckPamHasStartConfdir() bool {
	return C.check_pam_start_confdir() == 0
}

// CheckPamHasBinaryProtocol return if pam on system supports PAM_BINARY_PROMPT
func CheckPamHasBinaryProtocol() bool {
	return C.BINARY_PROMPT_IS_SUPPORTED != 0
}
