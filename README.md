# Shipyard

CLI em Go para distribuir e operar o ecossistema Shipyard a partir do terminal.

## Objetivo

O `shipyard` deve ser o ponto de entrada instalável para operações locais e remotas, com foco em:

- instalação simples via shell bootstrap
- experiência profissional de CLI com subcomandos previsíveis
- gestão de componentes auxiliares como `gateway`
- validação de ambiente, status, upgrade e troubleshooting

## Responsabilidades

O CLI em Go deve cuidar de:

- parsing de comandos e flags
- instalação e upgrade do binário
- download de releases versionadas
- bootstrap de componentes auxiliares
- escrita e validação de arquivos de configuração
- integração com `systemd`, cron e processos locais
- checks de saúde e diagnósticos

## Limites

O CLI não deve concentrar a lógica complexa do orquestrador.

O `gateway` pode continuar em Python, desde que o CLI seja responsável por:

- instalar uma versão explícita do projeto
- preparar `venv` e dependências
- provisionar config e diretórios
- registrar e operar serviço local
- expor comandos como `install`, `status`, `logs`, `upgrade` e `uninstall`

## Linha de comandos inicial

```bash
shipyard doctor
shipyard version
shipyard cron add
shipyard cron list
shipyard gateway install
shipyard gateway status
shipyard gateway upgrade
shipyard gateway uninstall
shipyard agent add
shipyard agent list
```

## Direção técnica

- linguagem: Go
- versão base de toolchain: Go 1.26.2
- distribuição: binário único por plataforma
- instalador: shell script mínimo, com a lógica principal no binário
- framework CLI sugerido: `cobra`
- config sugerida: `~/.config/shipyard/config.yaml`
- diretório de dados sugerido: `~/.local/share/shipyard/`
- logs sugeridos: `~/.local/state/shipyard/`

## Princípios

- preferir idempotência em instalações e upgrades
- evitar lógica crítica em shell script
- sempre suportar modo não interativo quando possível
- tratar Linux como alvo principal, sem impedir suporte posterior a macOS
- instalar componentes por release/tag, não por `main`
