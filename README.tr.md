# frpDash

[English](README.md) | **Türkçe**

[frp](https://github.com/fatedier/frp) tabanlı tünelleri merkezi olarak yönetmek için web arayüzlü, kendi
sunucunuzda çalışan bir yönetim aracı. Sunucudaki `frps` ile NAT arkasındaki makinelerde çalışan `frpc`
istemcilerini **yapılandırma dosyalarını elle düzenlemeden** tek bir panelden kurar, yapılandırır ve izler.

- Tek Go binary: `server` (VDS), `agent` (client makineler) ve gömülü web paneli
- **Add Client → tek satırlık `curl ... | sudo sh`** ile kurulum ve eşleştirme; agent frpc'yi kendisi indirir ve
  systemd servisi kurar
- **Public** servisler (tcp/udp, VDS'te otomatik boş port) ve **private** servisler (stcp, internete hiç açılmaz,
  yalnızca yetki verilen client'ler visitor olarak erişir)
- Port çakışması kontrolü: frpdash'in, diğer servislerin ya da VDS'teki başka programların kullandığı portlar reddedilir
- Güvenlik duvarı servisleri takip eder: yalnızca kullanılan portlar açık
- Online/offline durumu, panelde yapılan değişiklik saniyeler içinde agent'a gider
- Domain gerektirmez: agent bağlantısı self-signed TLS + sertifika parmak izi sabitleme ile korunur

**Hızlı başlangıç** (VDS'te):

```bash
curl -fsSL https://raw.githubusercontent.com/bymayfe/frpdash/main/install.sh | sudo sh
```

Ardından [Kurulum](#kurulum) bölümünü izle.

## Nasıl çalışır

```
          (SSH tüneli)                                  İnternet
 Tarayıcı ───────────────► 127.0.0.1:8090  Panel ┐
                                                 │  frpdash server (VDS)
 frpdash agent ──── wss://VDS:7443 ──► Agent API ┤   ├─ SQLite
   (ev, NAT arkası)    TLS + pin                 │   └─ frps  :7000  ──► public portlar 20000-20999
   └─ frpc ─────────────────────────────────────────►┘
```

- Panel **yalnızca loopback**'te dinler; internete açılmaz. SSH tüneliyle erişilir.
- Agent bağlantıyı kendisi başlatır (NAT arkasında çalışır). Sunucu her client için bir **istenen config** ve
  revizyon numarası tutar; agent bağlandığında ya da panelde değişiklik olduğunda bunu alır, `frpc verify` ile
  doğrular, uygular ve onay döner.
- Kullanıcı girdisi asla doğrudan frp config'ine yazılmaz: panel yapılandırılmış veri gönderir, agent doğrulayıp
  TOML'i bir kütüphaneyle üretir.
- frp binary'leri sabitlenmiş sürümden (**v0.72.0**) indirilir ve koda gömülü SHA-256 ile doğrulanır.

## Roller

Aynı `frpdash` binary'si iki yerde, iki rolde çalışır:

|                 | **Server**                                 | **Client**                                                  |
| --------------- | ------------------------------------------ | ----------------------------------------------------------- |
| Nerede          | VDS (internete açık sunucu)                | Evdeki PC, NAT arkasındaki her makine                       |
| frp tarafı      | **frps**                                   | **frpc**                                                    |
| Komut           | `frpdash server`                           | `frpdash connect` (bir kere), sonra `frpdash agent` servisi |
| systemd servisi | `frpdash-server`                           | `frpdash-agent`                                             |
| Ne yapar        | Paneli çalıştırır, frps'i kurar ve yönetir | frpc'yi kurar, panelden gelen ayarla çalıştırır             |

Akış: **VDS'e server kurulur → panelde "Add client" → panelin verdiği tek satır client'te çalıştırılır →
client VDS'e bağlanır.** Client'ten panele hiçbir şey yazılmaz.

## Kurulum

Gereken: internetten erişilebilen, systemd kullanan bir Linux sunucu (amd64 ya da arm64, bir VDS) ve ona SSH
erişimi.

### 1. Server: VDS'e kurulum (frps tarafı)

VDS'te:

```bash
curl -fsSL https://raw.githubusercontent.com/bymayfe/frpdash/main/install.sh | sudo sh
```

Betik son sürümü indirir, SHA-256 checksum'larını doğrular, `frpdash`'i `/usr/local/bin`'e kurar ve `frpdash-server`
servisini başlatır. Tek kullanımlık bir **kurulum kodu** basar.

**Kurulumu tarayıcıda bitir.** Panel internete hiç açılmaz; kendi bilgisayarından SSH tüneliyle aç:

```bash
ssh -L 8090:127.0.0.1:8090 kullanici@vds-adresin     # sonra http://localhost:8090
```

İlk sayfa şunları sorar:

1. **kurulum kodu** (VDS'te `/var/lib/frpdash/setup-code` dosyasında da var, yalnızca root okuyabilir),
2. **admin hesabı**,
3. client'lerin bağlanacağı **public adres** (sunucuda bulunan adresler öneri olarak gelir; sağlayıcı NAT
   kullanıyorsa public IP'yi yaz),
4. **güvenlik duvarı**: kutu işaretliyse frpdash gereken portları ufw veya firewalld'de açar (Debian/Ubuntu'da
   gerekirse ufw'yi kurar). Bir güvenlik duvarını etkinleştirmeden önce her zaman SSH portuna izin verir; sonrasında
   bir public servis eklenince portunu açar, silinince kapatır.

VDS'te kullanılan portlar: `7443/tcp` (client'ler ve client kurulum betiği), `7000/tcp` ve `7000/udp` (frps) ve her
public servisin portu. Sağlayıcının web panelinde ayrı bir güvenlik duvarı varsa bu portları orada da aç.

### Güncelleme ve kaldırma

Güncellemek için kurulum komutunu tekrar çalıştır; veritabanı, client'ler, servisler ve ayarlar korunur.

```bash
curl -fsSL https://raw.githubusercontent.com/bymayfe/frpdash/main/install.sh | sudo sh -s -- --version v0.1.0   # belirli bir sürüm
curl -fsSL https://raw.githubusercontent.com/bymayfe/frpdash/main/install.sh | sudo sh -s -- --uninstall        # kaldır, verileri koru
curl -fsSL https://raw.githubusercontent.com/bymayfe/frpdash/main/install.sh | sudo sh -s -- --purge            # her şeyi sil
```

`--uninstall` ve `--purge` client makinelerde de çalışır (`frpdash-agent`'ı kaldırır).

### 2. Client: evdeki PC'yi bağlama (frpc tarafı)

Her makine için bir kere:

1. **Panelde** (VDS): **Add client** → bir isim ver (ör. `ev-pc`). Sayfada tek satırlık bir komut çıkar.
2. **Evdeki PC'de**: o satırı terminale yapıştır:
   ```bash
   curl -fsSL -k --pinnedpubkey 'sha256//...' 'https://<VDS_IP>:7443/install/<kod>' | sudo sh
   ```
   Komut makineye uygun `frpdash`'i VDS'ten indirir, VDS ile eşleşir, kimlik bilgilerini
   `/etc/frpdash/agent.json` (0600) dosyasına yazar ve `frpdash-agent` servisini kurar; agent frpc'yi indirip
   çalıştırır.
3. **Panelde** client **online** görünür. Artık servisleri panelden eklersin; client'te başka bir şey yapmazsın.

Kod tek kullanımlık ve 15 dk geçerlidir. Sertifika self-signed olduğu için komutta `-k` var; curl'ü yalnızca
senin VDS'ine bağlayan `--pinnedpubkey`'dir, pin uymazsa hiçbir şey indirilmez.

curl yoksa: `frpdash` binary'sini client'e elle kopyala ve sayfadaki "Without curl" bölümündeki
`sudo ./frpdash connect <kod>` komutunu çalıştır.

### 3. Servis ekleme

Client sayfasında **Add service**:

- **public**: tcp veya udp. VDS portunu boş bırakırsan aralıktan ilk boş port atanır.
  Erişim: `VDS_IP:port`.
- **private**: stcp. VDS'te port açılmaz. Erişecek client'i seç ve onun makinesinde kullanılacak yerel portu
  yaz; o client'te `127.0.0.1:<port>` servise bağlanır.

## Komutlar

| Komut                                                   | Nerede               | Ne yapar                                          |
| ------------------------------------------------------- | -------------------- | ------------------------------------------------- |
| `frpdash server [--install-service] [--public-host IP]` | Server (VDS, frps)   | Panel + client bağlantıları + frps                |
| `frpdash passwd <kullanıcı>`                            | Server (VDS, frps)   | Panel parolasını değiştirir (unutulursa kurtarma) |
| `frpdash connect [--no-install] <kod>`                  | Client (ev PC, frpc) | Eşleşir, agent servisini kurar                    |
| `frpdash agent`                                         | Client (ev PC, frpc) | frpc'yi yönetir (normalde systemd çalıştırır)     |
| `frpdash version`                                       | Her yer              | Sürüm                                             |

Bayraklar için `frpdash <komut> -h`. Varsayılan dizinler: sunucu `/var/lib/frpdash`, agent
`/var/lib/frpdash-agent`. Loglar: `journalctl -u frpdash-server` / `journalctl -u frpdash-agent`
(frps/frpc çıktısı da buradadır).

## Güvenlik

- Parolalar bcrypt (cost 12); oturum, eşleştirme kodu ve agent secret'ları yalnızca SHA-256 hash olarak saklanır.
- Veritabanı ve anahtar dosyaları 0600, dizinler 0700.
- Panel: HttpOnly + SameSite=Strict oturum çerezi, cross-site POST reddi, sıkı CSP, loopback dışı adres reddi.
- Agent API: yalnızca TLS 1.3; eşleştirme, bağlantı ve kurulum uç noktaları. Agent sunucuyu sertifika parmak
  iziyle doğrular (devam ettirilen TLS oturumlarında da). Kurulum betiği ve binary yalnızca geçerli, kullanılmamış
  bir eşleştirme koduyla indirilebilir.
- Private servislere frps tarafında yalnızca panelde yetki verilen client'ler (`allowUsers`) bağlanabilir.

## Kaynaktan derleme

Go 1.26+ gerekir.

```bash
make build          # bin/frpdash
make build-linux    # bin/frpdash-linux-amd64 ve bin/frpdash-linux-arm64
make deploy-server VDS=kullanici@vds-adresin   # burada derle, SSH ile kopyala, VDS'te kur ya da güncelle
make css            # şablonlar değişince web/static/app.css'i yeniden üret (Tailwind CSS standalone CLI)
```

Sürümler `make release VERSION=vX.Y.Z` ile yayınlanır (GitHub CLI gerekir); `install.sh` bunlardan indirir.

## Sınırlamalar ve yol haritası

MVP tamam. Sonraki adımlar ve fikirler: [`docs/ROADMAP.tr.md`](docs/ROADMAP.tr.md).

Bilinen davranışlar: bir client'in config'i değişince o client'in frpc'si yeniden başlar (açık bağlantılar
kısa süre kopar). Agent root olarak çalışır.

## Lisans

[Apache Lisansı 2.0](LICENSE). Ayrıntılar: [NOTICE](NOTICE) ve [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

frpdash, [frp](https://github.com/fatedier/frp) (Apache-2.0) etrafında geliştirilmiş bağımsız bir projedir. frp'yi
içermez; resmi sürümü indirip checksum'ını doğrular. frp projesiyle bağlantılı değildir ve onun tarafından
desteklenmez.
