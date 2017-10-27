# hello-smtp
When I start learning a new programming language, my first "real" coding project is always a command-line SMTP submission tool. I've found this task to be complex enough to explore some of the nuance of the language and its standard libraries, while being simple enough to complete in a day or two and then move on. The functional requirements are well defined, which forces me to think through language idioms that might otherwise tempt me to warp the app's behavior to fit the language.

Fellow programmers have often asked me to share these little programs with them, so here they are. There are no build instructions, make files, or dependency lists; if you need help compiling one of these, then it isn't for you.

## Specification (man page)

### SYNOPSIS
   smtp [options] server mail_from rcpt1 [ rcpt2 ... rcptn ]
or:
   smtp [options] -s server -f mailfrom -r rcptto msg1 ... msgn
or:
   smtp [options] -s server -F msg1 ... msgn

The first form is the historic form and generally the most useful when
sending a single message that has a large number of recipients, e.g., a
distribution list expansion.  The second form is more useful for sending
a large number of messages to the same set of recipients, as might be
done in a test environment.  The third form is convenient for end-user
applications, but probably ill-advised for test applications because of the
risk of inadvertant spamming.

### DESCRIPTION
This script reads one or more fully-formed RFC-2822 messages and submits
them to the specified SMTP server.  The return address (`MAIL` `FROM`)
and recipient addresses (`RCPT` `TO`) may be specified using command line
options, or extracted from the message header.  Options are available
to support opportunitistic or mandatory TLS, and SMTP login (`SMTP` `AUTH`).
End-of-line characters are fully normalized to RFC-5321 requirements, so
message contents with either UNIX-style (newline) or DOS-style (CRLF) line
termination will be submitted correctly.

### ADDRESSING OPTIONS

<DL>
<DT>-f mailfrom
<DD>Specify the sender address (MAIL FROM) of the message.

<DT>-r recipient
<DD>Specify the recipient (RCPT TO) of the message. This option can be repeated as many times as needed.

<DT>-s server
<DD>Specify the SMTP server.

<DT>-F
<DD>Get MAIL FROM and RCPT TO from the message header. When this option is present the
  <TT>-f</TT> option can be used to override the MAIL FROM from the header. The <TT>-r</TT>
  option is forbidden.
</DL>

If any of these options are present, then the command line arguments are
interpretted as a list of message filenames to be submitted.  If none of
these are present, then the arguments are interpretted as the server name,
bounce address (MAIL FROM), and one or more recipients.

### GENERAL OPTIONS

<DL>
<DT>-a
<DD>Stop immediately (abort) if any recipients are rejected.  Normally the
   client will continue to send to the valid recipients.

<DT>-d
<DD>Disconnect between sending multiple messages.
  Normally a single connection is used to send all messages.

<DT>-e
<DD>Supress the default end-of-line normalization. This option should only be used
  for server testing as the effect can be unpredictable on messages bodies that are not
  fully normalized.
  
<DT>-h
<DD>Print the manual page and then stop

<DT>-H heloname
<DD>Use the specified string as the EHLO/HELO name.  Otherwise use the fully
    qualified for

<DT>-p port
<DD>Specify the port number to use. Default is 25.

<DT>-R
<DD>Prepend a standard `Received` header field to the body.

<DT>-t
<DD>Write the SMTP trace log to stdout.
  
<DT>-v
<DD>Write a single informational line to stdout for each connection and message sent.
</DL>

### SECURITY OPTIONS
<DL>
<DT>-U username
<DD>Name to use for SMTP AUTH.

<DT>-P password
<DD>Password to use for SMTP AUTH.

<DT>-T
<DD>Use SSL/TLS encryption when connecting to server (opportunistic).

<DT>-M
<DD>Use SSL/TLS encryption when connecting to server (mandatory).

<DT>-C cipherstring
<DD>Set the SSL cipherstring.
  
<DT>-V
<DD>Perform certifcate verification; abort if the verification fails. This is off by
  default because so few SMTP servers have correct certificates.
</DL>
  
Mandatory TLS (`-M` option) should always be used for `SMTP` `AUTH`, as the only portable mechanism
for exchange the login/password is `PLAIN`, which passes the password in the clear.

## Language Implementation Notes

The goal of each implementation is to use the language's standard libraries to
implement the complete set of features documented above, and do so in a way that is
robust in the face of errors. None of these implementations are complete: each
has some limitations within its standard libraries that prevent some features
from being implemented, as well as some error cases that are not handled correctly.

The following protocol bugs are common:

* RFC 5321 says when the server returns a 500 reponse to an EHLO command, clients _should_ retry with HELO.
  However, nearly all client libraries retry on _any_ error, discarding the original response code and thus masking
  root cause for the failure.
  
* Most client libraries handle server disconnects poorly. At a minimum, a proper client library should close
  the socket on a 421 response; a strong implementation will check for connection loss before each socket write
  to avoid writing data into a socket that is in TCP FIN state.

### Perl
The implementation is built around the `Net::SMTP` package.

* The -e option is not supported as the library always normalizes end-of-lines.

### Python
The implementation is built around the `smtplib` package.

* The Python library is one of the few that supports SMTP pipelining. The command line utility should take advantage of this, but currently does not.
* The library does not support incremental writes; so the entire message has to be slurped into memory.
* The implementation include a kludge to work around Issue25852.

### Go
The implementation is built around the `net/smtp` package.

* The -e option is not supported as the library always normalizes end-of-lines.
* The -t option is not supported as the library does not support protocol tracing. This is a surprising deficincy, given how well-written the package is otherwise.
