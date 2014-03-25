package gfsocket

import (
	"net"
	"net/textproto"
//	"bytes"
	"os"
	"fmt"
//	"io"
	"errors"
	"bufio"
	"strings"
	"strconv"
	"encoding/json"
	"log"
)

type Event struct {
	Type string
	Content DataContent
	ContentRaw string
} 

type Filter struct {
	Name string
	Value string
}


//Representacion Conexion al
//Softswitch por un socket
type Connection struct {
	conn net.Conn
	debug bool
	logger *log.Logger
	password string
	reader *textproto.Reader
	buffer *bufio.Reader
	
	apiChan chan ApiResponse
	cmdChan chan CommandReply

	handlers map[Filter][]func(interface{})
	handlersChan map[Filter][]chan interface{}


}


type HandleChan struct {
	handler func(fs *Connection, ch chan interface{})
	channel chan interface{}
}

type CommandReply struct {
	Status string
	Content string
}

type ApiResponse struct {
	Status string
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


const BUFFER_SIZE =  1024
/**
 *Conecta a mod_event_socket de freeswitch y utentica
 */
func NewFS(raddr string, password string) (*Connection, error) {
	conn, err := net.Dial("tcp4", raddr)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("Error connection: %s", err.Error()))
	}
	
	buf := bufio.NewReaderSize(conn, BUFFER_SIZE)
	reader := textproto.NewReader(buf)
		
	fs := &Connection{conn, false, log.New(os.Stderr, "gfsocket_", 0),
		password, reader, buf,
		make(chan ApiResponse, 1), 
		make(chan CommandReply, 1),
		make(map[Filter][]func(interface{})),
		make(map[Filter][]chan interface{}),
	}


	ev, _ := fs.recvEvent()
	if ev.Type == "auth/request" {

		fs.conn.Write([]byte("auth " + password + "\n\n"))
		evAuth, _ := fs.recvEvent()
		if evAuth.Content.Get("Reply-Text")[0:3] == "+OK" {
			go dispatch(fs)	
			return fs, nil
		}else{
			return nil, errors.New(fmt.Sprintf("Error authentication"))
		}

	}



	return nil, errors.New("Error is a freeswitch?")
	//return fs,nil
}

//Go Rutina, para leer mensajes de Freeswitch
//y procesarlos segun el caso
func dispatch(fs *Connection) {
	defer fs.conn.Close()
	for{
		header, err := fs.reader.ReadMIMEHeader()

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
	content_body := strings.TrimRight(string(BufferByte(fs.buffer, content_length)), "\n ")

	switch header.Get("Content-Type") {
	case "command/reply":
		reply_raw := header.Get("Reply-Text")
		fs.cmdChan <- CommandReply{reply_raw[0:strings.Index(reply_raw," ")],
			reply_raw[strings.Index(reply_raw," ")+1:]}

	case "api/response":
		var body string = content_body
		fs.apiChan <- ApiResponse{string(body[0:strings.Index(body," ")]),
			string(body[strings.Index(body," ")+1:])}

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
		dispatchHandlers(fs, DataContent(DataContentJSON{ev_body_json}) , &ev_send)
	default:
		ev_send := Event{header.Get("Content-Type"), header, content_body}
		dispatchHandlers(fs, header, &ev_send)
	}
}

func dispatchHandlers(fs *Connection, header DataContent, ev_send *Event) {
	for filter, funcs := range fs.handlers {
		if header.Get(filter.Name) == filter.Value {
			for _, fc := range funcs {
				if fc != nil {
					go fc(*ev_send)
				}
			}
		}
	}
	for filter, handlers := range fs.handlersChan {
		if header.Get(filter.Name) == filter.Value {
			for _, handle := range handlers {
				if handle != nil {
					handle <- *ev_send
				}
			}
		}
	}
}

//Autentica la conexion
//Ejecutar cuando sea necesario, ejemplo: reconexion
func (fs *Connection) authenticate() {
	fs.Send("auth " + fs.password)
}

//Crea un array de byte, tomados del Buffer (bufio)
func BufferByte(reader *bufio.Reader, length int) (dst []byte) {
	dst = make([]byte, length)
	for ix, _ := range dst {
		dst[ix], _ = reader.ReadByte()
	}
	return
}


func (fs *Connection) SetDebug(d bool) {
	fs.debug = d
}

func (fs *Connection) Debug() *log.Logger {
	return fs.logger
}

//Registra "listener" en base al "Filter", el cual se compara
//con las cabezeras enviadas por el Freeswitch.
func (fs *Connection) HandleFunc(filter Filter, handler func(interface{})){
	fs.handlers[filter] = append(fs.handlers[filter], handler)
}

//Registra "listener" en base al "Filter", pero se crea *chan* para el envio
//de los eventos y se corre *handler* como Go Rutina.
func (fs *Connection) HandleChanFunc(filter Filter, handler func(*Connection, chan interface{})) {
	ch := make(chan interface{}, 1)
	fs.handlersChan[filter] = append(fs.handlersChan[filter], ch)
	go handler(fs, ch)
}

//Ejecuta *api* de Freeswitch
func (fs *Connection) Api(args string) ApiResponse {
	fs.Send("api " + args)
	res, _ := <- fs.apiChan
	return res
}

//Ejecuta *bgapi* y retorna Job-UUId
func (fs *Connection) BGApi(args string, background_job func (interface{})) (BackgroundJob, error) {
	fs.Send("bgapi " + args)
	res, _ := <- fs.cmdChan
	if res.Status == "+OK" {
		jobUUID := strings.Trim(res.Content[strings.Index(res.Content,":")+1:], " ")
		if background_job != nil {
			fs.HandleFunc(Filter{"Job-UUID", jobUUID}, background_job)
		}
		return BackgroundJob{jobUUID}, nil
	}else{
		return BackgroundJob{""}, errors.New(res.Content)
	}
}

//Envia comando  Freeswitch y espera reply
func (fs *Connection) Cmd(cmd string) CommandReply {
	fs.conn.Write([]byte(cmd + "\n\n"))
	res, _ := <- fs.cmdChan
	return res
}

//Envia comando raw a Freeswitch
func (fs *Connection) Send(cmd string) {
	fs.conn.Write([]byte(cmd + "\n\n"))
}

func (fs *Connection) recvEvent() (Event, error){
	header, err := fs.reader.ReadMIMEHeader()
	ev := Event{header.Get("Content-Type"), header, ""}
	if err != nil {
		return ev, err
	}

	return ev, nil
}


