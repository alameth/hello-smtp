/*
gosmtp -- Submit a fully-formed RFC-2822 message from the command line

SYNOPSIS
	gosmtp [ options ] server mail_from rcpt1 [ rcpt2 ... rcptn ]
    or
	gosmtp [ options ] -s server -f mailfrom -r rcptto msg1 [ ... msgn ]
    or
	gosmtp [ options ] -s server -F msg1 [ ... msgn ]

    The first is the historic form and generally the most useful when sending a
    single message that has a large number of recipients, e.g., a distribution
    list expansion.  The second form is more useful for sending a large number
    of messages to the same set of recipients, as might be done in a test
    environment.  The third form is most convenient for end-user applications,
    but ill-advised for test applications because of the risk of inadvertant
    spamming.

DESCRIPTION
    This command line utility reads one or more fully-formed RFC-2822 messages
    and submits them to the specified SMTP server.  The bounce address (MAIL
    FROM) and recipient addresses (RCPT TO) caan be specified on the command
    line or extract from the message header.  Options are available to support
    TLS and SMTP login (SMTP AUTH).  End-of-line markers in the body are
    normalized to CRLF per RFC-5321.

IMPLEMENTATION NOTES
    This application relies on Paul Borman's getopt package, as the standard
    Golang getopt cannot support the interface spec.

    Lines are always normalized.  The underlying net/textproto implementation
    has no way to turn this off.

    Protocol tracing is not supported as there is no support for it in either
    the net/smtp package or the underlying net/textproto pkg.  AFAIK golang
    is the only standard library SMTP that fails to support tracing.

    net/smtp requires the remote server in ssh-style hostname:port notation.
    An ordinary user would reasonably expect the same notation available on
    the command line.  However, net/smtp's PlainAuth authenticator requires
    the hostname alone.  Rather than adding a bunch of GoLang-specific code
    to parse around this inconsistency, this implementation just follows the
    specification, and allows setting the port number only via the -p option.

BUGS
    The password prompt does not disable echo.  This is a horrible security
    botch, but difficult to fix as there is no mechanism in standard Go
    to turn off terminal echo, and the user-contributed libraries require
    much later versions of Go than what is bundled with Mint 17.  It would
    be possible by raw termio manipulation, but that seems a bit much for
    what is supposed to be a didactic exercise in the language.

    net/smtp handles errors on EHLO poorly, discarding the error (which most
    likely contains root cause) and retrying with HELO even on errors other
    than 500.  (This is a common bug across many language libraries.)
*/

package main

import (
    "crypto/tls"
    "bufio"
    "bytes"
    "errors"
    "fmt"
    "github.com/pborman/getopt"
    "io"
    "log"
    "net"
    "net/mail"
    "net/smtp"
    "os"
    "runtime"
    "strings"
    "time"
)

//
var shortUsage = "For help, type:\n    gosmtp -h"

var longUsage = `gosmtp: submit a fully formed RFC822 message to an SMTP server.

Usage:
    gosmtp [ options ] server mailfrom rcpt1 [ rcpt2 ... rcptn ]
or
    gosmtp [ options ] -s server -f mailfrom -r rcptto msg1 [ ... msgn ]
or
    gosmtp [ options ] -s server -F msg1 [ ... msgn ]

Options:`

// Command line options
//
var optAbortAnyBad = getopt.Bool('a', "Stop (abort) if any recipients are rejected")
var optIgnoreAllBad = getopt.Bool('c', "Continue even if all recipients are rejected")
var optDisconnect = getopt.Bool('d', "Disconnect between messages")
var optMailfrom = getopt.String('f', "", "Specify the sender address", "mailfrom")
var optEnvFromHeader = getopt.Bool('F', "Get envelope from message header")
var optHeloName = getopt.String('H', "", "Manually set the client's hostname (for EHLO)", "name")
var optPort = getopt.String('p', "25", "Override the default port number of 25", "port")
var optPassword = getopt.String('P', "", "Set password for SMTP authantication", "password")
var optRecipients = getopt.List('r', "Specify recipient addresses", "recipient")
var optAddReceived = getopt.Bool('R', "Prepend a standard Received header field")
var optMandatoryTLS = getopt.Bool('M', "Use TLS Encryption, abort if not available")
var optServer = getopt.String('s', "", "Specify the SMTP server", "server")
var optUseTLS = getopt.Bool('T', "Use TLS Encryption, with fallback to cleartext")
var optUser = getopt.String('U', "", "Use SMTP authentication", "username")
var optVerbose = getopt.Bool('v', "Write activity to stdout")

// Global errlog, a better way to log errors instead of plain stderr
var errlog = log.New(os.Stderr, "", 0)

// Encapsulate the smtp.Client with logic to maintain connection state between
// messages.
//
type clientwrapper struct {
    server     string
    port       string
    heloname   string
    user       string
    password   string
    client    *smtp.Client
    tlsConfig  tls.Config
}

// Send sends one message.  This code was liberated out of the main to simplify
// file management (making sure everything is closed) and make it easier to
// manage SMTP state between messages.
//
// This function logs and disposes of most errors, returning nil; permenant
// connection errors turn failure.
//
// This code "cheats" more than it should, grubbing around in the command line
// options.
//
func (cw *clientwrapper) Send(filename string, msgin *os.File,
	    mailfrom []string, recipients []string) (error) {
    var err error

    if msgin == nil {
	panic("Internal error: msgin file pointer not set")
    }

    wholeMsgin := bufio.NewReader(msgin)
    var headerByteBuf []byte

    if *optEnvFromHeader {
	// This seems a lot uglier than it should be.
	//
	// mail.ReadMessage() has a friendly interface that accepts a reader,
	// imports the header, and then returns a reader for the body.  This
	// allows the caller to avoid slurping the entire message into memory.
	// Unfortunately, the parser consumes and discards the original header,
	// which is what we need for writing to the wire.  So: slurp the header
	// into a byte slice, convert that to a Reader, and pass that to
	// ReadMessage().
	//
	for {
	    if line, err := wholeMsgin.ReadBytes('\n'); err != nil {
		errlog.Println(filename + ": ", err)
		return nil
	    } else {
		headerByteBuf = append(headerByteBuf, line...)
		if (line[0] == '\n') {
		    break;
		}
	    }
	}
	headerMsgin := bytes.NewReader(headerByteBuf)
	msg, err := mail.ReadMessage(headerMsgin)
	if (err != nil) {
	    errlog.Println(err)
	    return nil
	}
	header := msg.Header

	// It's OK if the header field is missing, not OK if the field exists
	// but is invalid.
	//
	if len(mailfrom) == 0 && len(header["From"]) > 0 {
	    if fromlist, err := header.AddressList("From"); err != nil {
		errlog.Println(filename + ": From:", err)
		return nil
	    } else {
		mailfrom = append(mailfrom, fromlist[0].Address)
	    }
	}
	if len(header["To"]) > 0 {
	    if tolist, err := header.AddressList("To"); err != nil {
		errlog.Println(filename + ": To:", err)
		return nil
	    } else {
		for _,oneaddr := range tolist {
		   recipients = append(recipients, oneaddr.Address)
		}
	    }
	}
	if len(header["Cc"]) > 0 {
	    if cclist, err := header.AddressList("Cc"); err != nil {
		errlog.Println(filename + ": Cc:", err)
		return nil
	    } else {
		for _,oneaddr := range cclist {
		   recipients = append(recipients, oneaddr.Address)
		}
	    }
	}
    }

    // If we aren't already connected, do so now.  EHLO, TLS, and AUTH are
    // considered to be logically part of connection establishment, so any
    // errors here result in the connection being dropped.
    //
    if cw.client == nil {
	if cw.client, err = smtp.Dial(cw.server + ":" + cw.port); err != nil {
	    errlog.Println(err)
	    return errors.New("Connection failed")
	}
	// Retries with HELO on any error, so if anything goes wrong the
	// error string is useless (and often empty)
	if err = cw.client.Hello(cw.heloname); err != nil {
	    cw.client.Close()
	    cw.client = nil
	    errlog.Println(err)
	    return nil
	}

	if *optUseTLS || *optMandatoryTLS {
	    if enabled,_ := cw.client.Extension("STARTTLS"); !enabled {
		if *optMandatoryTLS {
		    cw.client.Quit()
		    cw.client = nil
		    return errors.New("Server does not support TLS")
		} else {
		    errlog.Println("Server does not support TLS; falling back to clear text")
		}
	    } else if err = cw.client.StartTLS(&cw.tlsConfig); err != nil {
		errlog.Println(err)
		if *optMandatoryTLS {
		    cw.client.Quit()
		    cw.client = nil
		    return errors.New("Server rejected STARTTLS command")
		} else {
		    errlog.Println("Server rejected STARTTLS; falling back to clear text")
		}
	    }
	}

	if cw.user != "" {
	    // net/smtp requires the caller to pick the authenticator to use.
	    // We use PLAIN because it's the only one that's universal.  Note
	    // that net/smtp will error out if there's no TLS, and will close
	    // the connection if the authentication fails.
	    //
	    auth := smtp.PlainAuth("", cw.user, cw.password, cw.server)
	    err = cw.client.Auth(auth); if err != nil {
		cw.client.Close()
		cw.client = nil
		return err
	    }
	}
    }

    if len(mailfrom) == 0 {
	mailfrom = append(mailfrom, "")
    }
    fmt.Println("File:", filename, "From:", mailfrom, "To:", recipients)

    // Common exit handler.  The defer is set up after the connection has
    // been established but before starting the mail session.  This allows
    // subsequent code to return whenever it encounters an error, and let
    // the error handler close out the session correctly.
    //
    inMailState := false
    inDataState := false
    defer func() {
	if err != nil && strings.HasPrefix(err.Error(), "421") {
	    // 421 response means the server dropped the connection.
	    cw.client.Close()
	    cw.client = nil
	} else if inDataState {
	    // All errors in DATA require a silent connection drop
	    cw.client.Close()
	    cw.client = nil
	} else if *optDisconnect {
	    if err = cw.client.Quit(); err != nil {
		errlog.Println(err)
	    }
	    cw.client = nil
	} else if inMailState {
	    // This Email cannot be sent, but the next might, so reset for that
	    // If the RSET command fails, though, drop the connection.
	    if err = cw.client.Reset(); err != nil {
		errlog.Println(err)
		cw.client.Close()
		cw.client = nil
	    }
	}
    }()

    if err = cw.client.Mail(mailfrom[0]); err != nil {
	errlog.Println(err)
	return nil
    }

    inMailState = true
    nGoodRcpts := 0
    for _, rcpt := range recipients {
	if err = cw.client.Rcpt(rcpt); err != nil {
	    errlog.Println(err)
	    continue
	}
	nGoodRcpts++
    }
    if nGoodRcpts == 0 && !*optIgnoreAllBad {
	errlog.Println("Stopping; message has no valid recipients")
	return nil
    }
    if nGoodRcpts != len(recipients) && *optAbortAnyBad {
	errlog.Println("Stopping; message has bad recipients")
	return nil
    }

    msgout, err := cw.client.Data()
    if err != nil {
	errlog.Println(err)
	return nil
    }
    inDataState = true
    if *optAddReceived {
	t := time.Now()
	received := "Received: by " + cw.heloname +
	    " (gosmtp " + runtime.GOOS + " " + runtime.GOARCH + "); " +
	    t.Format(time.RFC1123Z) + "\r\n"
	if _, err = io.WriteString(msgout, received); err != nil {
	    errlog.Println(err)
	    return nil
	}
    }
    // Might have already read and buffered the header
    if len(headerByteBuf) != 0 {
	if _,err = msgout.Write(headerByteBuf); err != nil {
	    errlog.Println(err)
	    return nil
	}
    }
    // If header wasn't pre-buffered, this will write the entire message
    if _, err = io.Copy(msgout, wholeMsgin); err != nil {
	errlog.Println(err)
	return nil
    }

    msgout.Close()

    inDataState = false
    inMailState = false

    return nil
}

func (cw *clientwrapper) Close() {
    if cw.client != nil {
	if err := cw.client.Quit(); err != nil {
	    errlog.Println(err)
	}
    }
}

func main() {
    // Basic command line parse.  We leave getopt() blind about the different
    // command line formats, and infer the format after.  The different formats
    // also motivate us to use our own usage message.
    //
    pOptShowHelp := getopt.Bool('h', "Display help page")

    // getopt.Parse()
    if err := getopt.Getopt(nil); err != nil {
    	errlog.Println(err)
	errlog.Println(shortUsage)
	os.Exit(1)
    }

    if *pOptShowHelp {
	fmt.Println(longUsage)
	getopt.CommandLine.PrintOptions(os.Stdout)
	os.Exit(0)
    }

    // Infer which command line style was used based on the options that are
    // present, then check for the required arguments and set the connection
    // parameters accordingly.
    //
    // NOTE: With this getopt pkg, the only way to tell if a string option was
    //	     not set is via the IsSet() call.  This creates some ugliness in
    //	     explicitly checking for options by letter deep in the code.  (In
    //	     classic getopt(), an option that hasn't been set is nil.)
    //	     
    var server string
    var mailfrom, recipients, filenames []string

    if *optEnvFromHeader {
	// Format Three.  -s is required; -f is allowed; -r is forbidden
	if !getopt.IsSet('s') {
	    errlog.Println("Missing server name (-s server)")
	    errlog.Println(shortUsage)
	    os.Exit(-1)
	}
	if getopt.IsSet('f') {
	    mailfrom[0] = *optMailfrom	// Set to override what's in the file
	}
	if getopt.IsSet('r') {
	    errlog.Println("The -F and -r options cannot be used together")
	    errlog.Println(shortUsage)
	    os.Exit(-1)
	}
	server = *optServer
	filenames = getopt.Args()
    } else if getopt.IsSet('s') || getopt.IsSet('r') || getopt.IsSet('f') {
	// Format Two. All three of -s -r and -f are required
	if !getopt.IsSet('s') {
	    // errlog.Println("Missing server name (-s server)")
	    errlog.Println("Missing server name (-s server)")
	    errlog.Println(shortUsage)
	    os.Exit(-1)
	}
	if !getopt.IsSet('f') {
	    errlog.Println("Missing bounce address (-f address)")
	    errlog.Println(shortUsage)
	    os.Exit(-1)
	}
	if !getopt.IsSet('r') {
	    errlog.Println("Missing recipients (-r address)")
	    errlog.Println(shortUsage)
	    os.Exit(-1)
	}
	server = *optServer
	mailfrom = append(mailfrom, *optMailfrom)
	recipients = *optRecipients
	filenames = getopt.Args()
    } else {
	// Format One. Connection parameters from positional arguments.
	if getopt.NArgs() < 3 {
	    errlog.Println("Missing required arguments")
	    errlog.Println(shortUsage)
	    os.Exit(-1)
	}
	server = getopt.Arg(0)
	mailfrom = append(mailfrom, getopt.Arg(1))
	recipients = getopt.Args()[2:]
    }

    // If the user specified a HELO name, use that. Otherwise, try to use the
    // machine's FQDN, falling back to the plain hostname if we have to, and an
    // obvious garbage name as a last resort.
    //
    var heloname string
    var err error
    if getopt.IsSet('H') {
	heloname = *optHeloName
    } else if hostname, err := os.Hostname(); err == nil {
	if canonname, err := net.LookupCNAME(hostname); err == nil {
	    // Lookup usually appends a dot
	    heloname = strings.TrimRight(canonname, ".")
	} else {
	    heloname = hostname
	}
    } else {
	heloname = "gosmtp.example.com"
    }

    // If there's a user name but no password, prompt for the password.  Note
    // that a 0-length password is legal (if foolish), so a direct check of the
    // -U flag is needed.
    //
    // TODO/CRITICAL: Echo should be disabled, but there's no portable way to
    //    do that natively.  There are user libraries, but they require a much
    //    later version of GoLang than what's in Mint 17 LTS.
    //
    user := *optUser
    password := *optPassword
    if user != "" && !getopt.IsSet('P') {
	fmt.Print("Password: ")
	if termf, err := os.Open("/dev/tty"); err != nil {
	    errlog.Println(err)
	    os.Exit(-1)
	} else {
	    reader := bufio.NewReader(termf)
	    if passline, _, err := reader.ReadLine(); err != nil {
		errlog.Println(err)
		os.Exit(-1)
	    } else {
		password = string(passline)
	    }
	}
    }

    if *optVerbose {
	fmt.Println("Client:", heloname, "Server:", server, "TLS:", optUseTLS)
    }

    // The clientwrapper object holds all parameters that are reused across
    // connections/messages; it also maintains connection state.
    //
    // The Send() method only returns an error on fatal errors, like connection
    // failure or SMTP AUTH failure.
    //
    client := clientwrapper{server: server, port: *optPort,
	    heloname: heloname, user: user, password: password}

    if len(filenames) == 0 {
	// No files, so read from stdin
	err = client.Send("-", os.Stdin, mailfrom, recipients)
	if err != nil {
	    errlog.Println(err)
	    os.Exit(-1)
	}
    } else {
	// Process the input files, one file per message
	for _, filename := range filenames {
	    if msgin, err := os.Open(filename); err != nil {
		errlog.Println(err)
	    } else {
		err = client.Send(filename, msgin, mailfrom, recipients)
		if err != nil {
		    errlog.Println(err)
		    os.Exit(-1)
		}
		msgin.Close()
	    }
	}
    }

    client.Close()
}
