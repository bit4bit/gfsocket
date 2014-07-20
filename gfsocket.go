package gfsocket

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/textproto"
	"os"
	"strconv"
	"strings"
)

type Event struct {
	Type       string
	Content    DataContent
	ContentRaw string
}

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

type HandleFunc struct {
	Filter
	handler func(interface{})
}

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

type DataContentJSON struct {
	data map[string]string
}

func (data DataContentJSON) Get(name string) string {
	return data.data[name]
}

/**
 *Conecta a mod_event_socket de freeswitch y autentica
 */
func Dial(remoteaddr, password string) (*Connection, error) {
	conn, err := textproto.Dial("tcp4", remoteaddr)
	if err != nil {
		return nil, err
	}

	fs := &Connection{conn, false, log.New(os.Stderr, "gfsocket_", 0),
		password,
		make(chan ApiResponse, 1),
		make(chan CommandReply, 1),
		make([]HandleFunc, 0),
		make([]HandleChan, 0),
	}

	ev, _ := fs.recvEvent()
	if ev.Type == "auth/request" {

		fs.text.PrintfLine("auth %s\r\n", password)
		evAuth, _ := fs.recvEvent()
		if evAuth.Content.Get("Reply-Text")[0:3] == "+OK" {
			go dispatch(fs)
			return fs, nil
		} else {
			return nil, textproto.ProtocolError("error authentication")
		}

	}

	return nil, errors.New("error is a freeswitch?")
}

//Go Rutina, para leer mensajes de Freeswitch
//y procesarlos segun el caso
func dispatch(fs *Connection) {
	defer fs.text.Close()
	for {
		header, err := fs.text.ReadMIMEHeader()

		if err != nil {
			continue
		}
		if fs.debug {
			fmt.Println(header)
		}
		dispathActions(fs, header)
	}
}

func dispathActions(fs *Connection, header DataContent) {
	content_length, _ := strconv.Atoi(header.Get("Content-Length"))

	content_body := strings.TrimRight(string(bufferByte(fs.text.Reader.R, content_length)), "\n ")

	switch header.Get("Content-Type") {
	case "command/reply":
		reply_raw := header.Get("Reply-Text")
		fs.cmdChan <- CommandReply{reply_raw[0:strings.Index(reply_raw, " ")],
			reply_raw[strings.Index(reply_raw, " ")+1:]}

	case "api/response":
		var body string = content_body
		fs.apiChan <- ApiResponse{string(body[0:strings.Index(body, " ")]),
			string(body[strings.Index(body, " ")+1:])}

	case "auth/request":
		fs.authenticate()

	case "text/event-plain":
		buf := bufio.NewReader(strings.NewReader(content_body))
		reader := textproto.NewReader(buf)
		ev_body_mime, _ := reader.ReadMIMEHeader()
		ev_send := Event{header.Get("Content-Type"), ev_body_mime, content_body}
		dispatchHandlers(fs, ev_body_mime, &ev_send)

	case "text/event-json":
		json_decode := json.NewDecoder(strings.NewReader(content_body))
		ev_body_json := make(map[string]string)
		json_decode.Decode(&ev_body_json)
		ev_send := Event{header.Get("Content-Type"),
			DataContent(DataContentJSON{ev_body_json}),
			content_body}
		dispatchHandlers(fs, DataContent(DataContentJSON{ev_body_json}), &ev_send)
	default:
		ev_send := Event{header.Get("Content-Type"), header, content_body}
		dispatchHandlers(fs, header, &ev_send)
	}
}

func dispatchHandlers(fs *Connection, header DataContent, ev_send *Event) {
	for _, handle := range fs.handlers {
		if handle.Filter.And(header) {
			go handle.handler(*ev_send)
		}
	}

	for _, handle := range fs.handlersChan {
		if handle.Filter.And(header) {
			handle.handler <- *ev_send
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

func (fs *Connection) recvEvent() (Event, error) {
	header, err := fs.text.ReadMIMEHeader()
	ev := Event{header.Get("Content-Type"), header, ""}
	if err != nil {
		return ev, err
	}

	return ev, nil
}

//Crea un array de byte, tomados del Buffer (bufio)
func bufferByte(reader *bufio.Reader, length int) (dst []byte) {
	dst = make([]byte, length)
	for ix, _ := range dst {
		dst[ix], _ = reader.ReadByte()
	}
	return
}
