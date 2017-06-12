package gfsocket

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var errDisconnect = errors.New("disconnected")

type Package struct {
	Type    string
	Payload interface{}
}

// Evento freeswitch
type Event struct {
	Type       string
	Content    DataContent
	ContentRaw string
}

// Filter flitra eventos
type Filter map[string]string

//Verifica contenido con el filtro si no cumple
//alguna de las claves del filtro se da por invalido
//cumple un ==And==
func (filter Filter) And(data DataContent) bool {
	for k, v := range filter {
		if data.Get(k) != v {
			return false
		}
	}
	return true
}

//HandleFunc reacciona segun filtro
type HandleFunc struct {
	Filter
	handler func(interface{})
}

//HandleChan reaccion segun filtro enviando eventos en chan
type HandleChan struct {
	Filter
	handler chan interface{}
}

//Representacion Conexion al
//Softswitch por un socket
type Connection struct {
	text *textproto.Conn

	debug    bool
	logger   *log.Logger
	password string

	apiChan chan ApiResponse
	cmdChan chan CommandReply

	handlers     []HandleFunc
	handlersChan []HandleChan

	cancelFunc context.CancelFunc
}

type CommandReply struct {
	Status  string
	Content string
}

type ApiResponse struct {
	Status  string
	Content string
}

type BackgroundJob struct {
	JobUUID string
}

type DataContent interface {
	Get(name string) string
}

type DataContentMIMEHeader struct {
	textproto.MIMEHeader
}

func (data DataContentMIMEHeader) Get(name string) string {
	val, err := url.QueryUnescape(data.MIMEHeader.Get(name))
	if err != nil {
		return data.MIMEHeader.Get(name)
	}
	return val
}

type DataContentJSON struct {
	data map[string]string
}

func (data DataContentJSON) Get(name string) string {
	val, err := url.QueryUnescape(data.data[name])
	if err != nil {
		return data.data[name]
	}
	return val
}

// Reusa conexion
func NewConn(cxt context.Context, raw io.ReadWriteCloser, password string) (*Connection, error) {
	conn := textproto.NewConn(raw)
	lcxt, cancelFunc := context.WithCancel(cxt)
	fs := &Connection{conn, false,
		log.New(os.Stderr, "gfsocket_", 0),
		password,
		make(chan ApiResponse, 1),
		make(chan CommandReply, 1),
		make([]HandleFunc, 0),
		make([]HandleChan, 0),
		cancelFunc,
	}

	pkg, _ := fs.recvPackage()
	if pkg.Type == "auth/request" {

		fs.text.PrintfLine("auth %s\r\n", password)
		pkg, _ := fs.recvPackage()
		if pkg.Payload.(CommandReply).Status == "+OK" {
			go dispatch(lcxt, fs)
			return fs, nil
		} else {
			return nil, textproto.ProtocolError("error authentication")
		}

	}

	return nil, errors.New("error is a freeswitch?")
}

/**
 *Conecta a mod_event_socket de freeswitch y autentica
 */
func Dial(remoteaddr, password string) (*Connection, error) {
	conn, err := net.Dial("tcp", remoteaddr)
	if err != nil {
		return nil, err
	}
	return NewConn(context.Background(), conn, password)
}

/**
 *Conecta a mod_event_socket de freeswitch y autentica
 */
func DialTimeout(remoteaddr, password string, timeout time.Duration) (*Connection, error) {
	conn, err := net.DialTimeout("tcp", remoteaddr, timeout)
	if err != nil {
		return nil, err
	}
	return NewConn(context.Background(), conn, password)
}

//Go Rutina, para leer mensajes de Freeswitch
//y procesarlos segun el caso
func dispatch(cxt context.Context, fs *Connection) {
	defer fs.text.Close()

loop:
	for {
		select {
		case <-cxt.Done():
			break loop
		default:
			pkg, err := fs.recvPackage()
			if fs.debug {
				fs.logger.Printf("==READ FROM FREESWITCH==\n")
			}
			if err != nil {
				if err == errDisconnect {
					fs.Close()
					break loop
				} else {
					panic(err)
				}
			}

			dispathActions(fs, pkg)
			if fs.debug {
				fs.logger.Printf("==END READ FROM FREESWITCH==\n")
			}
		}
	}

}

func dispathActions(fs *Connection, pkg *Package) {

	switch pkg.Type {
	case "text/disconnect-notice":
		fs.Close()
	case "command/reply":
		fs.cmdChan <- pkg.Payload.(CommandReply)

	case "api/response":
		fs.apiChan <- pkg.Payload.(ApiResponse)

	case "auth/request":
		fs.authenticate()

	case "text/event-plain":
		dispatchHandlers(fs, pkg.Payload.(Event))

	default:
		ev := pkg.Payload.(Event)
		dispatchHandlers(fs, ev)
	}
}

func dispatchHandlers(fs *Connection, ev Event) {
	for _, handle := range fs.handlers {
		if handle.Filter.And(ev.Content) {
			go handle.handler(ev)
		}
	}

	for _, handle := range fs.handlersChan {
		if handle.Filter.And(ev.Content) {
			handle.handler <- ev
		}
	}

}

//Autentica la conexion
//Ejecutar cuando sea necesario, ejemplo: reconexion
func (fs *Connection) authenticate() {
	fs.Send("auth " + fs.password)
}

func (fs *Connection) SetDebug(d bool) {
	fs.debug = d
}

func (fs *Connection) Debug() *log.Logger {
	return fs.logger
}

//Registra "listener" en base al "Filter", el cual se compara
//con las cabezeras enviadas por el Freeswitch.
func (fs *Connection) HandleFunc(filter Filter, handler func(interface{})) {
	fs.handlers = append(fs.handlers, HandleFunc{filter, handler})
}

//Registra "listener" en base al "Filter", pero se crea *chan* para el envio
//de los eventos y se corre *handler* como Go Rutina.
func (fs *Connection) HandleChanFunc(filter Filter, handler func(*Connection, chan interface{})) {
	ch := make(chan interface{}, 1)
	fs.handlersChan = append(fs.handlersChan, HandleChan{filter, ch})
	go handler(fs, ch)
}

//Ejecuta *api* de Freeswitch
func (fs *Connection) Api(args string) ApiResponse {
	fs.Send("api " + args)
	res, _ := <-fs.apiChan
	return res
}

//Ejecuta *api* de Freeswitch
func (fs *Connection) ApiChan(args string) chan ApiResponse {
	fs.Send("api " + args)
	return fs.apiChan
}

//Ejecuta *bgapi* y retorna Job-UUId
func (fs *Connection) BGApi(args string, background_job func(interface{})) (BackgroundJob, error) {
	fs.Send("bgapi " + args)
	res, _ := <-fs.cmdChan
	if res.Status == "+OK" {
		jobUUID := strings.Trim(res.Content[strings.Index(res.Content, ":")+1:], " ")
		if background_job != nil {
			fs.HandleFunc(Filter{"Job-UUID": jobUUID}, background_job)
		}
		return BackgroundJob{jobUUID}, nil
	} else {
		return BackgroundJob{""}, errors.New(res.Content)
	}
}

//Envia comando  Freeswitch y espera reply
func (fs *Connection) Cmd(cmd string) CommandReply {
	fs.text.PrintfLine("%s \r\n", cmd)
	res, _ := <-fs.cmdChan
	return res
}

//Envia comando raw a Freeswitch
func (fs *Connection) Send(cmd string) {
	fs.text.PrintfLine("%s \r\n", cmd)
}

func (fs *Connection) Close() {
	fs.cancelFunc()
}

func (fs *Connection) recvPackage() (*Package, error) {
	header, err := fs.text.ReadMIMEHeader()

	if err != nil {
		return nil, err
	}

	content_length, _ := strconv.Atoi(header.Get("Content-Length"))

	content_body := strings.TrimRight(string(bufferByte(fs.text.Reader.R, content_length)), "\n ")
	if fs.debug {
		fs.logger.Print(content_body)
	}

	switch header.Get("Content-Type") {
	case "text/disconnect-notice":
		fs.Close()
		return nil, errDisconnect

	case "command/reply":
		reply_raw := header.Get("Reply-Text")
		cmd := CommandReply{reply_raw[0:strings.Index(reply_raw, " ")],
			reply_raw[strings.Index(reply_raw, " ")+1:]}
		return &Package{"command/reply", cmd}, nil

	case "api/response":
		result := strings.SplitN(strings.TrimSpace(content_body), " ", 2)
		var api ApiResponse
		if len(result) == 2 {
			api = ApiResponse{result[0], result[1]}
		} else {
			api = ApiResponse{"UNKNOWN", content_body}
		}
		return &Package{"api/response", api}, nil

	case "auth/request":
		return &Package{"auth/request",
			Event{"auth/request", nil, ""}}, nil

	case "text/event-plain":
		buf := bufio.NewReader(strings.NewReader(content_body))
		reader := textproto.NewReader(buf)
		ev_body_mime, _ := reader.ReadMIMEHeader()
		ev_send := Event{header.Get("Content-Type"), DataContentMIMEHeader{ev_body_mime}, content_body}
		return &Package{"text/event-plain", ev_send}, nil

	case "text/event-json":
		json_decode := json.NewDecoder(strings.NewReader(content_body))
		ev_body_json := make(map[string]string)
		json_decode.Decode(&ev_body_json)
		ev_send := Event{header.Get("Content-Type"),
			DataContent(DataContentJSON{ev_body_json}),
			content_body}
		return &Package{"text/event-plain", ev_send}, nil
	default:
		ev := Event{header.Get("Content-Type"), header, content_body}
		return &Package{header.Get("Content-Type"), ev}, nil
	}
}

//Crea un array de byte, tomados del Buffer (bufio)
func bufferByte(reader *bufio.Reader, length int) (dst []byte) {
	dst = make([]byte, length)
	for ix, _ := range dst {
		dst[ix], _ = reader.ReadByte()
	}
	return
}
