# ViaPost CLI

CLI oficial da [ViaPost](https://viapost.io), distribuída por GitHub Releases.
Ela usa o SDK Go oficial e mantém a chave de API apenas no ambiente do processo.

> Beta `v0.1.x`: a interface pública pode receber ajustes antes da versão 1.0.

## Instalação

Baixe o arquivo do seu sistema na [release mais recente](https://github.com/ViaPost-io/viapost-cli/releases/latest), valide-o com `SHA256SUMS`, extraia o binário e mova-o para um diretório do seu `PATH`.

Exemplo para macOS Apple Silicon:

```bash
VERSION=0.1.0
curl -fLO "https://github.com/ViaPost-io/viapost-cli/releases/download/v${VERSION}/viapost_${VERSION}_darwin_arm64.tar.gz"
curl -fLO "https://github.com/ViaPost-io/viapost-cli/releases/download/v${VERSION}/SHA256SUMS"
grep "viapost_${VERSION}_darwin_arm64.tar.gz" SHA256SUMS | shasum -a 256 -c -
gh attestation verify "viapost_${VERSION}_darwin_arm64.tar.gz" \
  --repo ViaPost-io/viapost-cli \
  --signer-workflow ViaPost-io/viapost-cli/.github/workflows/release.yml \
  --source-ref "refs/tags/v${VERSION}" \
  --deny-self-hosted-runners
tar -xzf "viapost_${VERSION}_darwin_arm64.tar.gz"
install -m 0755 viapost /usr/local/bin/viapost
```

Também é possível compilar uma tag diretamente:

```bash
go install github.com/ViaPost-io/viapost-cli/cmd/viapost@v0.1.0
```

O build a partir do código requer Go 1.26.6 ou superior, versão mínima que contém as correções de segurança exigidas pelo projeto.

## Configuração

```bash
export VIAPOST_API_KEY="vp_live_..."
```

Variáveis opcionais:

- `VIAPOST_BASE_URL` — padrão `https://api.viapost.io`;
- `VIAPOST_TIMEOUT` — duração Go positiva, padrão `60s`.

A chave não possui flag de linha de comando, evitando exposição no histórico e na lista de processos. Use uma chave de servidor com os menores scopes necessários e nunca a inclua em scripts versionados.

## Uso

```bash
viapost send \
  --from hello@example.com \
  --to person@example.com \
  --subject "Olá" \
  --text "Mensagem enviada pela ViaPost" \
  --idempotency-key order-123

# Para conteúdo sensível, evite corpo/assunto na linha de comando:
viapost send --data @request.json --idempotency-key order-123
cat message.txt | viapost send \
  --from hello@example.com --to person@example.com --subject "Olá" --text-file -

viapost messages list --status delivered --period 7d --limit 20
viapost messages get MESSAGE_ID
viapost usage
viapost --pretty usage
viapost completion zsh
```

Resultados dos comandos funcionais e erros são JSON; ajuda e scripts de completion são texto.
Leituras `GET` podem ser repetidas até três vezes em respostas transitórias (`408`, `429` e
`5xx`); `send` nunca é repetido automaticamente. Para repetir um envio de forma segura, forneça a
mesma `--idempotency-key`.

`--data @arquivo` carrega o objeto JSON completo sem colocar o conteúdo na lista de processos.
`--data -`, `--text-file -` e `--html-file -` leem de stdin. Os flags inline `--subject`, `--text`
e `--html` são convenientes para testes, mas não devem receber conteúdo sensível em ambientes
multiusuário.

## Códigos de saída

| Código | Significado |
|---:|---|
| 0 | sucesso |
| 2 | argumentos inválidos |
| 3 | configuração inválida ou ausente |
| 4 | erro retornado pela API |
| 5 | erro local ou de transporte |

## Segurança e suporte

- HTTPS é obrigatório fora de loopback; redirects e cookies não são aceitos pelo SDK.
- Respostas são limitadas e erros incluem `status`, `code` e `request_id` quando disponíveis.
- Verifique checksums e a attestação de proveniência da release antes de instalar.
- Consulte [SECURITY.md](SECURITY.md) para reportar vulnerabilidades.

Licença MIT. Documentação da API: [docs.viapost.io](https://docs.viapost.io).
