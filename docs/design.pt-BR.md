# Projeto

## Motivação

Uma outbox da aplicação não exercita snapshots exportados, limites de transação pgoutput e feedback de replicação. Por isso a origem é PostgreSQL real e o destino é SQLite consultável independentemente. O esquema fixo mantém as regras revisáveis. O projeto contém 42 arquivos com propósito: implementação, regressões, fixtures SQL sintéticas, demonstração local, CI de testes, políticas e documentação bilíngue. Não há workflow de publicação ou implantação.

## Processos

O controlador possui diretório privado e postmaster em primeiro plano. Ele inicia seu próprio executável como outro processo worker; os testes fazem o mesmo com o executável de testes e uma função específica. Assim não se compila um novo programa durante os cenários offline. O alvo da morte é um processo real, com conexão SQLite, lock exclusivo e conexão de replicação próprios.

stdin/stdout transportam solicitações/respostas JSON limitadas; fd 3 recebe liberação de barreira, fd 4 emite nomes de barreira e fd 5 recebe configuração privada limitada. Não existem connection strings fornecidas. Barreiras: snapshot após importação e antes da cópia, commit antes do commit SQLite, feedback depois do commit e antes do reconhecimento.

As consultas de origem e projeção do controlador são independentes. No cenário de atomicidade, uma transação de leitura fixa a imagem antiga. Outra consulta vê a imagem antiga antes do commit. Depois da liberação, uma leitura nova vê somente a imagem final e a leitura fixada continua vendo a antiga.

## Estados duráveis

| Estado | Imagem visível | Próxima ação |
|---|---|---|
| Vazio | sem metadados e sem linhas | bootstrap explícito |
| Bootstrap em andamento | imagem anterior completa | substituir atomicamente ou falhar |
| Pronto | linhas, identidade, checkpoint e contador | status/catchUp/bootstrap/stop |
| Buffer de transação | imagem anterior inalterada | Commit suportado ou erro |
| Confirmado sem feedback | imagem nova completa | reconhecer ou repetir na retomada |
| Catch-up falhou | última imagem completa | inspecionar; repetir se permitido |
| Rebootstrap necessário | imagem preservada, histórico inseguro | bootstrap explícito ou stop |

Não há transação parcialmente persistida. PostgreSQL guarda a origem; SQLite guarda projeção/checkpoint; flock só existe durante o processo. A captura bruta recente é efêmera e limitada.

Bootstrap e apply usam uma transação SQLite cada. O contador cresce uma vez por Commit novo, nunca por linha, snapshot inicial, rollback ou duplicata. Checkpoints são TEXT hexadecimal canônico de 16 posições, cobrindo todo uint64 sem conversão para int64. Transações duplicadas ainda passam por validação: endpoint antigo não torna mensagem inválida aceitável.

A retomada verifica identidade, publicação, plugin/banco/invalidação do slot e cronologia. Parte de confirmed_flush_lsn, permitindo testar repetição após perda de feedback. Relações são reconstruídas de mensagens reais, sem metadados antigos implícitos. Não se tenta recuperar failover geral ou adulteração maliciosa local; ausência de histórico é erro explícito com a imagem preservada.

WAL/FULL e ordem de feedback não provam resistência a falhas de hardware. O teste usa SIGKILL no worker após commit, não perda de energia. O postmaster é encerrado e recolhido antes de remover seus arquivos. Catch-up incompleto é explícito. Um experimento bem-sucedido inclui operações que falham intencionalmente. CI baixa dependências de compilação; cenários em runtime são locais/offline, com versão/source ID SQLite reais no relatório.
