# frpDash — Planlar

[English](ROADMAP.md) | **Türkçe**

Yapılacak işler. Biten maddeler işaretlenir; ayrıntılı tasarım kararları [`PLAN.md`](PLAN.md)'de.

## Kurulum

- [x] **Client için tek satırlık kurulum.** Panel "Add client" ekranında
      `curl ... --pinnedpubkey ... https://VDS:7443/install/<token> | sudo sh` verir. Betik frpdash'i VDS'ten
      indirir (amd64/arm64), eşleşir ve `frpdash-agent` servisini kurar. Token tek kullanımlık, 15 dk geçerli;
      curl sertifikayı açık anahtar pin'iyle doğrular.
- [x] **İlk kurulum sihirbazı (parametresiz server kurulumu).** `frpdash server --install-service` bayraksız
      çalışır; servis "kurulum bekliyor" modunda başlar (frps ve 7443 kapalı). Panel ilk açılışta tek kullanımlık
      kurulum kodunu (root'a ait `setup-code` dosyası + journal + kurulum çıktısı), admin hesabını ve public adresi
      (sunucudaki adresler öneri olarak, özel IP'ler işaretli) sorar. Public adres DB ayarında; `--public-host` ve
      `FRPDASH_ADMIN_*` sihirbazı atlamak için kalır.
- [x] **Kurulum sihirbazında ve Ayarlar'da güvenlik duvarı.** Gereken portları listeler; ufw veya firewalld'de
      açar ya da ufw'yi kurar (apt-get). Bir güvenlik duvarı etkinleştirilmeden önce SSH portlarına (`sshd -T` /
      `sshd_config`) her zaman izin verilir; çalıştırılan komutlar gösterilir.
- [x] **Güvenlik duvarı servisleri takip eder.** Yalnızca kullanılan portlar açık: public servisin VDS portu servis
      eklenince açılır, silinince kapanır (artık aralığın tamamı açılmıyor). frpdash açtıklarını hatırlar ve yalnızca
      onları kapatır; SSH'a ve elle eklenen kurallara dokunmaz.
- [x] **Kaynak koddan kurulum.** `make deploy-server` derler, SSH ile kopyalar, kurar ya da günceller; adres
      verilmezse sorar, sudo'suz root girişlerinde de çalışır.
- [ ] Sihirbazda/Ayarlar'da portları (frps bind, agent, public aralık) düzenleme.
- [ ] Public adresi Ayarlar'dan değiştirme ve eşleşmiş client'lere iletme (şu an yalnızca `--public-host` ile;
      mevcut client'ler eski adresi bilir).
- [ ] **Ortak kurulum girişi ve rol tespiti.** Tek `install.sh` / `frpdash setup`: makinede agent.json varsa
      "kurulu client olarak devam et / yeniden eşleştir / kaldır", server DB'si varsa "güncelle", hiçbiri yoksa
      "Server (VDS, frps) mı Client (frpc) mı?" sorar; client seçilirse panelden alınan kodu ister.
- [ ] **`frpdash status`.** Makinenin rolünü ve durumunu gösterir: client'te server adresi, bağlantı, rev,
      frpc durumu ve servisler; server'da frps ve bağlı client'ler. Yalnızca okur.
- [x] **VDS'i tek komutla kurma (`make deploy-server VDS=vds`).** Derler, SSH ile kopyalar,
      `frpdash server --install-service` çalıştırır, diğer mimarinin binary'sini `/var/lib/frpdash/dist/`'e koyar.
      Tekrar çalıştırmak günceller.
- [x] **Tek satırlık server kurulumu.** `install.sh` sürüm binary'lerini indirir, checksum'larını doğrular; frpdash'i
      kurar, günceller (`--version`) ya da kaldırır (`--uninstall`, `--purge`). Sürümler: `make release`.
- [ ] **Client'i panelden SSH ile kurma.** Panelde client'in SSH adresi ve kullanıcısı girilir; server
      SSH ile bağlanıp kurulum satırını kendisi çalıştırır. Kullanıcı client'te hiçbir şey yazmaz.
      Açık sorular: SSH anahtarı nerede tutulur (tercihen hiç saklanmaz, yalnızca o istek için ajan/parola),
      host key doğrulaması, sudo parolası.
- [ ] Sunucu güncellemesi: yeni binary'yi atınca client'lerin agent'larını da güncelleme.

## Bağlantı ve ayarlar

- [ ] Global/client ayarlarını panelden düzenleme; bağlantı ayarlarını (bindPort, token, protokol)
      **30 sn geri almalı** uygulama. Protokol ve katman modeli hazır.
- [ ] Kapsam seçici ("sadece server / tüm client'ler / seçili client'ler") ve uygulamadan önce önizleme.
- [ ] frps plugin hook'u ile makine başına token.

## Client tarafından yönetim

Bu bölümdeki her şey **Developer mode** arkasındadır.

- [ ] **Developer mode.** Ayarlar sayfasının altında bir anahtar, varsayılan **kapalı**. Açılınca menüye
      "Developer" bölümü eklenir ve ek seçenekler görünür. Kapatınca yalnızca gizlenmez, server bu özellikleri
      uygulamayı da bırakır (ör. client düzenleme yetkileri geçersiz olur); ayarlar silinmez, tekrar açınca
      geri gelir. Açma/kapama loglanır. İleride eklenecek deneysel/ileri seçenekler de buraya girer.
- [ ] **İki anahtar: server + client.** Client tarafı özellikler yalnızca hem server'da hem o client'te
      Developer mode açıksa çalışır. Client'te açma server'a sorulur: server kapalıysa ya da client offline'sa
      açılmaz ("önce server'da açılmalı"). Server'da kapatılınca tüm client'lerde etkisi anında biter (server
      client'lere bildirir, client isteklerini reddeder). Karar her zaman server'da verilir.
- [ ] **Görünürlük.** Client kendi anahtar durumunu server'a bildirir. Panel: Ayarlar'da server anahtarı ve
      açık olan client'ler; client listesinde "Dev" sütunu (durum + yetki); client sayfasında
      "Server: açık · Bu client: açık" ve yetki seçimi. Client: yerel panelde üst şerit, `frpdash status`'ta
      "Developer" satırı; açma/kapama `frpdash dev on|off` veya yerel panelden.
- [ ] **Client yetki seviyesi** (panelde client başına, varsayılan en kısıtlı): _Yok_ (yalnızca durum) /
      _Görüntüle_ (kendi servisleri, portları, logları) / _Kendi servislerini düzenle_ (ekle, sil, local port,
      izin verilen aralıkta VDS portu). Global ayarlar, başka client'ler, kendi port aralığı ve kendini visitor
      ekleme hiçbir seviyede yok. Yetki her istekte server'da kontrol edilir.
- [ ] **Değişiklik server üzerinden.** Client'teki değişiklik agent bağlantısıyla server'a istek olarak gider;
      server yetki + port/aralık kontrolü yapar, kaydeder, rev artırır, config'i geri iter. Tek doğru kaynak
      server DB'si; client offline iken değişiklik yapılmaz. Her değişiklik "client tarafından" diye loglanır.
- [ ] **Client yerel paneli ve CLI.** Client'in `127.0.0.1`'inde yalnızca o makinenin servisleri; aynı işler
      için CLI komutları.
- [ ] **Client'ten VDS paneline erişim.** Client başına anahtar (varsayılan kapalı): agent bağlantısı üzerinden
      tünel, client'te `localhost:8090` → VDS paneli. Panel girişi yine zorunlu; giriş denemelerine hız sınırı.

## Panel

- [x] Arayüz Tailwind CSS v4 ile (standalone CLI, `make css`; çıktı binary'ye gömülü, CDN yok).
- [ ] Dinleyen port tarayıcı (client'te açık portları listeleyip servis eklemeyi kolaylaştırma).
- [ ] Panelde agent/frpc log ekranı.
- [ ] Giriş denemelerine hız sınırı.
- [ ] TOTP ile iki adımlı giriş.
- [ ] Değişiklik geçmişi.

## Daha sonra

- [ ] HTTP/domain desteği.
- [ ] Servis şablonları.
- [ ] Windows agent.
