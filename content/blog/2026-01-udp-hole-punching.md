---
title: "UDP Hole Punching"
date: 2026-01-08
tags: [networking, golang, p2p, nat]
summary: "Making two computers behind NATs talking directly to each other. What could possibly go wrong."
---

After playing with tools like Tailscale and Headscale for work for the past
couple of years, then deploying my own Headscale server for private LAN 
parties, I've always wanted to see what it took to build my own version. It
turns out that it's not all that difficult. We just need to fuck with NAT
traversal and have a centralised signaling server, though there are a few
caveats to it.

## High Level NAT

We currently live in the world where we use IPv4 addresses, the problem with
that is that they are 32bit numbers so there are a maximum limit of 4.3 billion
addresses available, which is just a bit more than the number of devices that
run Java. Because there are significantly more devices in the world we 
developed a way to have devices share IP addresses and segmenting local and 
private ranges:

- **Class A**: `10.0.0.0/8` (around 16 million)
- **Class B**: `172.16.0.0/12` (around 1 million)
- **Class C**: `192.168.0.0/16` (around 65 thousand, often used in homes)
- **Loopback**: `127.0.0.0/8` (used by a single device to refer to itself)
- **Link-Local**: `169.254.0.0/16` (used for APIPA)

Class A to C are the ones that are used in local networks and were NAT is 
typically used. So what is NAT? Network Address Translation, a routing table 
used by your networks router. When you make a request to the internet it first
gets stopped by the router, the router will then masquerade the source IP
address of the connection to it's own public IP address and pick a random 
unused port on itself. This information is added to the table, so when the 
destination server makes a response back to the router on that specific IP and 
port, the router knows that this traffic needs to be routed to the original
device on the local network. The destination server has no idea what your 
local device's IP address is, if it did then it would probably try and send a 
response to a random device on it's local network and/or just be dropped. 

So all local devices on your network will share the public IP address of your 
router. These NAT table entries are a mapping and will expire after some time
unless the connection has been closed/reset. So a table might look like this:

| Internal Address     | Router Port  | Proto | Expires |
| -------------------- | ------------ | ----- | ------- |
| `192.168.1.50:12345` | `:32421`     | TCP   | 86400s  |
| `192.168.1.62:9000`  | `:54332`     | UDP   | 300s    |
| `192.168.1.32:8080`  | `:443`       | TCP   | no exp  |

You may notice the last entry is a bit weird, and its because it is a static
NAT entry. Essentially this is port forwarding, we are forwarding any traffic
to the router's public ip address on port 443 and routing it to the web server
or reverse proxy on `192.168.1.32:8080`. These static NAT entries are created
on the router manually and are persistent.

## Time to Hole Punch

So now that we know that a NAT entry is automatically created for connections
going outbound we need a way to utilise this and we can do this by asking a 
question:

> What if 2 devices created a socket connection outbound, found out what their
> public IP address is AND the router port for the NAT, then told a signal 
> server the `<public_ip>:<router_port>` and then can get each others and 
> started sending data to each other via these routes?

This is the basic concept of how we can use hole punching to directly connect
two devices in two different internal/local networks together. 

This is also why we need to use UDP and not TCP. UDP is stateless which means
we can have a single socket and send data to who ever we want via the same
socket. We can't do this with TCP as it is stateful, if we create a connection
to find out our hole punched `<public_ip>:<router_port>` is then we can't use
the same connection to talk to a different device which means that will get 
allocated a different hole punched `<public_ip>:<router_port>` which we don't 
want.

## STUN Servers

Programs like Tailscale and Zerotier utilise STUN servers (Session Traversal
Utilities for NAT). There are a handful of these public servers where you send
a UDP packet to the server (typically port `:3478`) and it will respond with
the *public ip* and *public port* for that connection. This is only used for
discovery, so the program can create a single socket, discover it's server
reflexive address and then it can report this to a signal server. The protocol
is quite simple and can be found in the 
[RFC 5389](https://datatracker.ietf.org/doc/html/rfc5389) doc. 

## Lazy Signal Server

Because I am being lazy, I'm going to not follow the STUN protocol just steal
it's concept. I will also build this into the signal server as well, so the 
signal server will have a connection that it will read from constantly for 
messages an if it receives a `register <id>` then it will store it in a map 
for later. I'm not going to have it respond with its server reflexive address
as I'm just going to have the signal server stateful.

```go
var peers map[string]*net.UDPAddr

// handleRegisterCmd receives a new UDP connection from a remote host and their
// identity, stores it in our client registry and responses to the client. This
// should be called on a `register <id>`.
func handleRegisterCmd(conn *net.UDPConn, remoteAddr *net.UDPAddr, args ...string) {
    assert(conn != nil, "connection must not be nil")
    assert(remoteAddr != nil, "remote address must not be nil")

    // Validate the args
    if len(args) != 1 {
        conn.WriteToUDP([]byte("err invalid register cmd"), remoteAddr)
        return
    }

    // Store the peer
    peers[args[0]] = remoteAddr
    conn.WriteToUDP([]byte("ok"), remoteAddr)
}
```

Security? What's that? This is all for demo purposes, in reality you would
setup or build a proper protocol and even a way to verify or authenticate with
the signal server before registering.

I guess I better explain what I plan to build. This signal server will have a
simple protocol which clients can use to establish communications. Here is the
protocol:

| Command                     | Dir    | Description                                                                 |
| --------------------------- | ------ | --------------------------------------------------------------------------- |
| `register <id>`             | C -> S | Allow a client to register with their id                                    |
| `connect <from_id> <to_id>` | C -> S | Allows a client to establish a connection between peers                     |
| `punch <id> <addr>`         | S -> C | The server is telling a client to hole punch a connection to a certain peer |
| `hello <id>`                | C -> C | A hello message for punching clients holes (no-op)                          |
| `msg <id> {message}`        | C -> C | Clients talking to each other                                               |
| `keepalive`                 | C -> [S,C] | A keepalive message to keep the NAT entry fresh (no-op)                     |  
| `ok`                        | S -> C | A successful message                                                        |
| `err {message}`             | S -> C | An error message                                                            |

We have already done the `register` command the only other command the server
needs is the `connect` as the `keepalive` is a no-operation command which. The
`connect` command will look up both peer id's and then send a punch command to
each peer to establish connections.

```go
var peers map[string]*net.UDPAddr

// handleConnectCmd will check if the peers requested to connect are
// first registered then will establish a connection between them by telling
// them to punch each other.
func handleConnectCmd(conn *net.UDPConn, remoteAddr *net.UDPAddr, args ...string) {
    assert(conn != nil, "connection must not be nil")

    // Validate the args
    if len(args) != 2 {
        conn.WriteToUDP([]byte("err invalid connection cmd"), remoteAddr)
        return
    }

    // Fetch Peers
    peer1, found1 := peers[args[0]]
    peer2, found2 := peers[args[1]]

    if !found1 || !found2 {
        conn.WriteToUDP([]byte("err invalid peers"), remoteAddr)
        return
    }

    // Establish connections
    conn.WriteToUDP(fmt.Appendf([]byte{}, "punch %s %s", args[1], peer2.String()), peer1)
    conn.WriteToUDP(fmt.Appendf([]byte{}, "punch %s %s", args[0], peer1.String()), peer2)
}
```

The server then can just be served and handle the commands accordingly. This
is what it would look like:

```go
package main

const listenAddr = ":9000"

var peers map[string]*net.UDPAddr

func main() {
    laddr, _ := net.ResolveUDPAddr("udp", listenAddr)
    conn, _ := net.ListenUDP("udp", laddr)
    defer conn.Close()

    fmt.Println("[server] listening on :9000")

    handlers := map[string]func(*net.UDPConn, *net.UDPAddr, ...string){
        "register":  handleRegisterCmd,
        "connect":   handleConnectCmd,
        "keepalive": func(*net.UDPConn, *net.UDPAddr, ...string) { /* NOP */ },
    }

    peers = make(map[string]*net.UDPAddr)

    buf := make([]byte, 1024)
    for {
        n, raddr, err := conn.ReadFromUDP(buf)
        if err != nil {
            continue
        }

        args := strings.Fields(string(buf[:n]))
        if len(args) == 0 {
            continue
        }
        
        fn, ok := handlers[args[0]];
        if !ok {
            fmt.Printf("[client] unknown command %s\n", args[0])
            continue
        }

        // Handle Command
        fn(conn, raddr, args[1:])
    }
}
```

This isn't what you call production grade code, it can easily be destroyed if
you even look at it the wrong way. But it does everything we need for this 
demo.

## Lazy Client Time?

Yeah, this will be just as simple as the server. The difference is that we 
need to add some logic for the hole punching. When a client gets told by the 
server to punch an other client it will need to send a handful of `hello` 
packets to the target client, the first few will get dropped by the peer b's 
NAT it isn't until both peers try to talk to each other the then the NAT 
should be happy (depending on your NAT setting).


```go
const (
    punchDuration = 3 * time.Second
    punchInterval = 100 * time.Millisecond
)

var peers map[string]*net.UDPAddr

// handlePunchCmd will attempt to holepunch a remote peer with a given
// identity and address. It will also store the new peer in the
func handlePunchCmd(conn *net.UDPConn, _ *net.UDPAddr, args ...string) {
    assert(conn != nil, "connection must not be nil")

    // Validate the args
    if len(args) != 2 {
        return
    }

    // Check already connected
    if _, ok := peers[args[0]]; ok {
        return
    }

    // Resolve the addr string to a net addr
    peer, err := net.ResolveUDPAddr("udp", args[1])
    if err != nil {
        return
    }

    // Store the peer as reference for later
    peers[args[0]] = peer
    
    // Begin sequence to holepunch peer
    go func() {
        fmt.Printf("[client] punching peer %s (%s)\n", args[0], args[1])

        deadline := time.Now().Add(punchDuration)
        for time.Now().Before(deadline) {
            conn.WriteToUDP([]byte("hello"), peer)
            time.Sleep(punchInterval)
        }

        go startKeepaliveWith(conn, peer)

    }()
}
```

The last command is the `handleMessageCmd` which will just print the message 
to screen in the `[peer <id>] <message>` so I wont write the function in this 
post. The only thing left is the interface for how the user uses the system,
I think we can just do a lazy standard in command system:

```go
func handleUserInput(conn *net.UDPConn, signalServer *net.UDPAddr, identity string) {
    assert(conn != nil, "connection must not be nil")
    scanner := bufio.NewScanner(os.Stdin)

    for {
        fmt.Print("> ")
        if !scanner.Scan() {
            return
        }

        args := strings.Fields(scanner.Text())
        switch args[0] {
            case "/connect": // Handle Connect
               conn.WriteToUDP(fmt.Appendf([]byte{}, "connect %s %s", identity, args[1]), signalServer)

            default: // Handle Message
                msg := strings.Join(args, " ")
                for _, peer := range peers {
                    conn.WriteToUDP([]byte("msg " + identity + " " + msg), peer)
                }
        }
    }
}
```

The only thing left before putting the client together is to have a background
process to send a keep alive to the signal server and the peers that they have
connected to. We need to do this every so often, let's say every `10s` to 
make sure the NAT holepunch entries remain fresh and don't expire. I wont
write the function for this as it is just a for loop with a 10s sleep.

```go
package main

const serverAddr = "holepunch.lachlancox.dev:9000"

var peers map[string]*net.UDPAddr

func main() {
    identity := os.Args[1] // ./client <id>

    laddr, _ := net.ResolveUDPAddr("udp", ":0")
    conn, _ := net.ListenUDP("udp", laddr)
    defer conn.Close()

    peers = make(map[string]*net.UDPAddr)

    fmt.Printf("[client] listening on %s", conn.LocalAddr())

    handlers := map[string]func(*net.UDPConn, *net.UDPAddr, ...string){
        "punch":     handlePunchCmd,
        "message":   handleMessageCmd,
        "hello":     func(*net.UDPConn, *net.UDPAddr, ...string) { /* NOP */ },
        "keepalive": func(*net.UDPConn, *net.UDPAddr, ...string) { /* NOP */ },
        "ok":        func(*net.UDPConn, *net.UDPAddr, ...string) { /* NOP */ },
        "err":       logErrCmd,
    }

    buf := make([]byte, 1024)
    go func() {
        for {
            n, raddr, err := conn.ReadFromUDP(buf)
            if err != nil {
                continue
            }

            args := strings.Fields(string(buf[:n]))
            if len(args) == 0 {
                continue
            }

            fn, ok := handlers[args[0]];
            if !ok {
                fmt.Printf("[client] unknown command %s\n", args[0])
                continue
            }

            // Handle Command
            fn(conn, raddr, args[1:])
        }
    }()

    server, err := net.ResolveUDPAddr("udp", serverAddr)
	assert(err == nil, "failed to resolve server address "+serverAddr)

    conn.WriteToUDP([]byte("register " + identity), server)

    handleUserInput(conn, server, identity)
}
```

## Demo Time

Let's see this thing in action. I've deployed the signal server to
`holepunch.lachlancox.dev:9000` and I'm going to run two clients on completely
different networks to see if they can talk to each other.

**Setup:**
- **Signal Server**: Running on a VPS with a public IP
- **Client A (alice)**: My home network behind a standard home router
- **Client B (bob)**: A random work VM in our infra, don't tell anyone

> **Note:** The IPs shown below aren't the real ones, I'm not about to dox 
> myself that hard.

First, spin up the signal server and have both clients register:

```text
$ ./server
[server] listening on :9000
```

Alice connects from her home network and immediately tries to connect to Bob
with `/connect bob`. The server looks up both peers and tells them to punch
each other:

```text
$ ./client alice
[client] listening on [::]:52341
> /connect bob
[client] punching peer bob (203.45.167.89:31447)
> Hello, World?
> [peer bob] gday
```

On Bob's side, the punch command arrives and he starts sending `hello` packets
back. Once both NATs have seen outbound traffic to each other, the hole is
punched and messages flow directly:

```text
$ ./client bob
[client] listening on [::]:48892
> [client] punching peer alice (118.209.53.122:54832)
[peer alice] Hello, World?
gday
>
```

And there it is. Alice at `118.209.53.122` and Bob at `203.45.167.89` are now
talking directly to each other, their packets bypassing the signal server
entirely. The signal server's only job was to introduce them, after that
it's out of the picture.

## When It All Falls Apart

This demo worked because both Alice and Bob are behind "friendly" NATs. Not
all NATs play nice.

**Symmetric NAT** is the party killer. Unlike the NATs we've been dealing with,
symmetric NAT assigns a *different* external port for each destination. So when
Alice talks to the signal server she gets `118.209.53.122:54832`, but when she
tries to talk to Bob she might get `118.209.53.122:61203`. The port we told Bob
to punch is now useless.

**CGNAT** (Carrier-Grade NAT) is when your ISP puts you behind *another* layer
of NAT. Your router has a "public" IP that's actually private (usually in the
`100.64.0.0/10` range), and the ISP's NAT sits between you and the real
internet. Now you've got two NATs to punch through, and if either of them is
symmetric, you're cooked.

This is why tools like Tailscale don't just rely on hole punching. When direct
connections fail, they fall back to TURN relays - servers that sit in the
middle and forward traffic between peers. It's slower and costs bandwidth, but
at least it works. The trick is trying hole punching first and only falling
back to relays when necessary.

## What's Next

This was a quick and dirty proof of concept. If you wanted to turn this into
something actually usable, there's a bit more work to do:

- **Timestamps and presence** - Track when peers were last seen, clean up stale
  entries, show online/offline status
- **TUN/TAP and WireGuard** - Instead of sending text messages, create a
  `tun/tap` device and establish a WireGuard tunnel between peers. The signal
  server handles public key exchange and coordination, then gets out of the way
- **Proper STUN support** - Instead of baking discovery into the signal server,
  use actual STUN servers so clients can discover their reflexive address
  independently
- **Authentication** - Anyone can register as any identity right now, which is
  obviously terrible. Tokens, certificates, or even just a shared secret would
  be a start
- **A real protocol** - Design something with proper framing, versioning, and
  error handling instead of space-delimited strings

But for understanding how NAT traversal works under the hood, this gets the
job done. The core concept is simple: make both sides think they initiated the
connection, and the NAT will let the packets through.
