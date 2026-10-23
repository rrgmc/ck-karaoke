# CK Karaoke

[![CI](https://github.com/rrgmc/ck-karaoke/actions/workflows/ci.yml/badge.svg)](https://github.com/rrgmc/ck-karaoke/actions/workflows/ci.yml)

[English](README.md) | **Português (Brasil)**

O CK Karaoke transforma seu celular ou computador em um controle remoto para o
seu aparelho de karaokê. Ele funciona com a família **Videoke VSK** e com
aparelhos vendidos com as marcas CooKara e RafKara.

Com ele você pode:

- navegar e pesquisar a lista de músicas do aparelho por título, número ou
  artista;
- adicionar músicas à fila do aparelho, e ver e editar a fila;
- controlar a reprodução, o tom, o andamento e os volumes da música, do
  microfone e do eco;
- guardar favoritos em pastas, e compartilhar uma pasta com outro celular por
  QR code.

A interface do aplicativo está em inglês. Este guia mostra os nomes dos botões
como aparecem na tela.

<p>
  <img src="docs/screenshots/search.png" width="190" alt="Pesquisa na lista de músicas">
  <img src="docs/screenshots/artists.png" width="190" alt="A lista de artistas">
  <img src="docs/screenshots/now.png" width="190" alt="Controles de reprodução">
  <img src="docs/screenshots/queue.png" width="190" alt="A fila">
</p>

## Download

Baixe a versão mais recente na
[página de versões](https://github.com/rrgmc/ck-karaoke/releases/latest).

| Plataforma | Arquivo | Observações |
|---|---|---|
| Android 8 ou superior | `ck-karaoke-<versão>-android.apk` | Permita instalar aplicativos pelo navegador ou gerenciador de arquivos quando o Android pedir. |
| Windows 10 ou 11 | `ck-karaoke-<versão>-windows-amd64-setup.exe` | Se o SmartScreen avisar, escolha **Mais informações** e depois **Executar assim mesmo**. |
| macOS 10.13 ou superior | `ck-karaoke-<versão>-macos-universal.dmg` | Arraste o aplicativo para Aplicativos. Na primeira vez, abra com o botão direito, **Abrir**. |
| iPhone e iPad, iOS 15 ou superior | `ck-karaoke-<versão>-ios-unsigned.ipa` | É preciso assinar o arquivo antes que o iOS o instale; veja [Installing the release IPA](ports/ios/README.md#installing-the-release-ipa) (em inglês). |
| Linux: Ubuntu 22.04, Debian 12 ou superior | `ck-karaoke-<versão>-linux-amd64.deb` | Instale com `sudo apt install ./ck-karaoke-<versão>-linux-amd64.deb`, que também instala as bibliotecas necessárias. |

O aplicativo para macOS é assinado e autenticado pela Apple. O aplicativo para
Windows não é assinado pela Microsoft, por isso o Windows mostra um aviso na
primeira vez que você o abre. O arquivo `SHA256SUMS.txt` na página de versões
permite conferir se um download está íntegro.

Em outras distribuições Linux, compile o aplicativo a partir do código-fonte;
veja [docs/BUILDING.md](docs/BUILDING.md) (em inglês).

## Primeiros passos

1. Ligue o aparelho de karaokê e conecte-o à sua rede Wi-Fi.
2. Conecte seu celular ou computador à **mesma** rede.
3. Abra o CK Karaoke. Ele encontra o aparelho sozinho.
4. Na primeira vez, o aplicativo baixa a lista de músicas do aparelho. Isso
   leva alguns segundos e acontece só uma vez.

Em um iPhone, iPad ou Mac, permita o acesso à rede local quando o sistema
pedir. Sem isso o aplicativo não encontra o aparelho.

O ponto no canto inferior direito fica verde enquanto o aplicativo está
conectado ao aparelho, e vermelho quando não está.

## Usando o aplicativo

O aplicativo tem três abas na parte de baixo: **Songs** (músicas), **Now**
(tocando agora) e **Queue** (fila).

### Songs

A aba **Songs** lista todas as músicas do aparelho.

- Digite um título ou o número de uma música na caixa de pesquisa.
- Escolha o idioma das músicas na lista abaixo da caixa de pesquisa, e a ordem
  com **A–Z**.
- Toque em **Artists** para listar os artistas. Toque em um artista para ver
  as músicas dele.
- Toque em **+** em uma música para adicioná-la ao fim da fila do aparelho.
- Toque no botão de vídeo para procurar a música no YouTube.
- Toque em **⋯** para mostrar também **Play next** (tocar a seguir) e
  **Play now** (tocar agora) em cada música. Eles ficam ocultos por padrão,
  para que ninguém passe uma música na frente das outras por engano.

### Now

A aba **Now** mostra a música que está tocando e controla o aparelho:

- **Pause** (pausar), **Next** (próxima), **Skip** (pular), **Stop** (parar) e
  **Repeat** (repetir);
- **Key** (tom) e **Tempo** (andamento), com **reset** para voltar ao
  original;
- os volumes **Song** (música), **Mic** (microfone) e **Echo** (eco);
- **Melody**, que liga ou desliga a melodia-guia.

### Queue

A aba **Queue** mostra as músicas que aguardam para tocar. Toque em **✕** para
remover uma música.

### Favoritos

Toque em **☆** em uma música para colocá-la em uma pasta de favoritos. Toque em
**★** no topo da aba **Songs** para ver suas pastas. Use **+ Folder** para
criar uma nova.

Os favoritos ficam guardados em cada celular ou computador, não no aparelho.

<p>
  <img src="docs/screenshots/folder.png" width="190" alt="Uma pasta de favoritos">
  <img src="docs/screenshots/share.png" width="190" alt="O QR code de uma pasta">
</p>

Para copiar uma pasta para outro celular:

1. No celular que tem as músicas, abra a pasta, toque em **Sync** e escolha
   **Send**. Ele mostra um QR code.
2. No outro celular, abra a pasta que vai receber as músicas, ou crie uma
   nova. Toque em **Sync**, escolha **Receive** e aponte a câmera para o
   código.

O celular que recebe adiciona as músicas que ainda não tem. Nenhum dos dois
celulares remove músicas. Para deixar as duas pastas iguais, repita os passos
no sentido contrário.

Para guardar uma cópia de todas as pastas, toque em **Backup** na lista de
pastas. Um backup é um arquivo XML que você pode salvar e importar depois em
qualquer celular ou computador.

## Solução de problemas

**O aplicativo não encontra o aparelho.** Confira se o aparelho e o seu
celular ou computador estão na mesma rede Wi-Fi. Alguns roteadores isolam os
dispositivos em uma rede de convidados. Em um Mac, confira se o CK Karaoke tem
acesso à rede local em **Ajustes do Sistema**, **Privacidade e Segurança**,
**Rede Local**. Em um iPhone ou iPad, confira em **Ajustes**, **Privacidade e
Segurança**, **Rede Local**.

**O aplicativo desconecta depois de um tempo no Android.** Quando o aplicativo
fica em segundo plano por dez minutos, ele fecha a conexão com o aparelho.
Abra o aplicativo de novo para reconectar.

**Reinstalei o aplicativo e meus favoritos sumiram.** Desinstalar o aplicativo
apaga os favoritos dele. Faça um backup antes de desinstalar.

## Para desenvolvedores

A documentação para desenvolvedores está em inglês.

- [docs/BUILDING.md](docs/BUILDING.md): organização do repositório, o servidor
  de depuração de linha de comando, compilação dos aplicativos, testes e
  versões.
- [docs/LIBRARY.md](docs/LIBRARY.md): como usar os pacotes Go no seu próprio
  programa.
- [docs/PROTOCOL.md](docs/PROTOCOL.md): o protocolo do aparelho.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): como o servidor e os
  aplicativos se encaixam.

## Autor

Rangel Reale ([realerangel@gmail.com](mailto:realerangel@gmail.com))
