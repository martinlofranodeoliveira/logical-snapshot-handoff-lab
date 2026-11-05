# Logical Snapshot Handoff Lab

Versão de desenvolvimento **0.1.0**, por **Lofrano Tecnologia**. MIT, implementação original. [English](README.md) · [Site oficial](https://lofranotecnologia.com.br).

Experimento restrito de projeção PostgreSQL 17 → SQLite. Executa um PostgreSQL real e privado, um replicador Go separado e um controlador com consultas SQL independentes. Testes e demonstração não utilizam serviços externos, endpoints fornecidos, modelos, APIs, telemetria ou rede. A instalação das dependências precisa de rede quando não há cache local.

Não é um conector CDC geral nem uma afirmação de prontidão para produção. O objetivo é tornar verificável a passagem do snapshot para o fluxo de alterações.

## Instalação e execução

Requisitos: Linux, **UID sem privilégios**, Go 1.27+ (CI solicita Go 1.27), compilador C, binários do PostgreSQL **17** em `/usr/lib/postgresql/17/bin` e SQLite externo **>=3.51.3** com seus headers correspondentes. A compilação precisa da tag `libsqlite3`; o SQLite antigo embutido é rejeitado. Não execute como root. O cluster é criado exclusivamente em `/tmp/lsh-*`, privado e pertencente ao processo, com apenas socket Unix privado.

Leia [dependências e qualificação](docs/dependencies.md). Os módulos diretos estão fixados em `go.mod` e `go.sum` foi resolvido pelas ferramentas oficiais do Go; nenhum checksum foi inventado. Resolva e verifique o grafo com as ferramentas Go antes do uso offline:

```sh
export CGO_ENABLED=1
export GOFLAGS='-tags=libsqlite3'
# Use o diretório real da sua instalação SQLite, se não for a padrão:
export CGO_CFLAGS='-I/opt/sqlite/include'
export CGO_LDFLAGS='-L/opt/sqlite/lib -Wl,-rpath,/opt/sqlite/lib'
export LD_LIBRARY_PATH=/opt/sqlite/lib
export GOMAXPROCS=2
export GOMEMLIMIT=256MiB
go mod tidy
go mod verify
go test -p 1 ./...
go run ./cmd/demo
```

Omita as três configurações `/opt/sqlite` se o compilador e o carregador já usam a instalação qualificada. As variáveis se aplicam aos dois comandos. O relatório informa a versão e o source ID realmente vinculados, comparando funções da biblioteca com consultas SQL. O fornecimento destes arquivos não afirma que houve execução ou qualificação prévia das dependências.

Os testes do applier e do decodificador são offline. A integração executa quando há PG17 e UID não root. `LAB_INTEGRATION=1 go test -p 1 ./...` transforma ausência desses requisitos em erro, em vez de pular o teste. A demonstração nunca pula requisitos.

## Demonstração

Oito cenários sequenciais verificam snapshot exportado, repetição de captura real, atualização/remoção atômicas, rollback sem transação marcadora, morte após commit antes do feedback, truncate transacional, limite de alterações e perda do slot.

Trecho da saída humana, sem inventar LSN ou source ID:

```text
Logical Snapshot Handoff Lab — development 0.1.0
PASS: exported snapshot handoff
PASS: captured transaction replayed twice
PASS: atomic update/delete and pinned inspector
PASS: rollback without marker transaction; explicit incomplete target
PASS: process kill after commit before feedback; duplicate endpoint ignored
PASS: transactional truncate and insert
PASS: 65-change transaction rejected without checkpoint advance
PASS: missing slot requires explicit rebootstrap, preserving projection
PASS: all 8 sequential scenarios; source/projection equality asserted before intentional failures
```

A última linha de stdout é JSON, com `version`, `passed_scenarios`, `sqlite_version`, `sqlite_source_id`, `checkpoint` e `incomplete`. Falhas incluem `error`. Identificadores do PostgreSQL, LSN e source ID variam; a ordem dos cenários e os valores esperados são fixos. Código de saída 0 significa que todas as verificações passaram, incluindo falhas esperadas; não significa que o estado final, danificado intencionalmente, esteja sincronizado.

## Exemplos pequenos

As APIs são internas ao repositório. Não existe endpoint remoto ou opção de connection string. O controlador envia JSON por pipes herdados:

```json
{"action":"bootstrap","target":"0/0","barrier":"snapshot","timeout_ms":5000}
```

```json
{"action":"catchUp","target":"0/1234","barrier":"","timeout_ms":1000}
```

`catchUp` só tem sucesso quando o checkpoint durável alcança o alvo. Um WAL maior no servidor não prova esse resultado. `status` lê um snapshot SQLite completo; `stop` fecha o escritor. `--worker` exige descritores herdados do controlador e não é uma interface para bancos arbitrários.

Exemplo do applier dentro do repositório:

```go
tx := model.Transaction{End: endpoint, Changes: []model.Change{
    {Kind: "update", Item: model.Item{ID: 1, Label: "A2", Revision: 2}},
    {Kind: "delete", Item: model.Item{ID: 2}},
}}
applied, err := projection.Apply(ctx, tx, nil)
```

`projection` precisa de imagem inicial e identidade. `endpoint` vem de `Commit.TransactionEndLSN`, nunca de relógio ou posição de transporte.

## Decisões e limites

O exportador permanece válido e intocado enquanto outra transação repeatable-read importa o snapshot antes de consultar dados. SQLite publica imagem, ponto consistente e identidade atomicamente. O replay valida e acumula a transação completa, confirma linhas e checkpoint com WAL/FULL e só então envia feedback. Um lock exclusivo durante toda a vida do escritor impede dois replicadores; leitores usam transações de snapshot.

Apenas `public.items` é admitida: chave bigint, label UTF-8 sem NUL de até 256 bytes, revisão positiva, até 32 linhas finais, 64 operações e 64 KiB de payload decodificado por transação. Não há mudança de chave, migração de esquema, placeholders TOAST, tuplas binárias, tipos/publicações arbitrários, two-phase commit ou recuperação automática de slot perdido.

Configurações de WAL e thresholds SQLite são orientações, não cotas físicas de disco. A durabilidade depende do sistema operacional e do armazenamento. Matar um processo não testa perda de energia; repetição idempotente não é transporte exactly-once. Não há proteção contra adulteração maliciosa dos metadados locais ou failover da origem.

[Contrato](docs/contract.pt-BR.md) · [Projeto](docs/design.pt-BR.md) · [Protocolo](docs/protocol.pt-BR.md) · [Testes](docs/testing.md) · [Contribuição](CONTRIBUTING.md) · [Segurança](SECURITY.md).
