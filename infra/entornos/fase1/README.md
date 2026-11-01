# Fase 1: el sistema en una EC2

El mismo Docker Compose de la notebook, en **una** máquina en AWS, con la
demo del QR abierta a internet. **Efímero:** se crea para la sesión y se
destruye al terminar. Diseño y decisiones: el diagrama en
[`docs/adr/img/arquitectura-aws.png`](../../../docs/adr/img/arquitectura-aws.png)
(la Fase 1 es una sola máquina de esa arquitectura).

```
Internet ──► Internet Gateway ──► VPC 10.20.0.0/16 ──► subred pública
                                                        └── EC2 c7i.xlarge (on-demand)
                                                              ├─ security group: solo el puerto 80
                                                              ├─ rol IAM: SSM + leer el dataset
                                                              └─ arranque: Docker → repo → dataset → demo
bucket efímero con el dataset ◄── lo sube Terraform (apply) y lo borra (destroy)
```

| Archivo | Qué tiene |
|---|---|
| `versiones.tf` | Terraform, el provider de AWS y las etiquetas (`proyecto=drts`, `entorno=fase1`) |
| `variables.tf` | las entradas: tipo de instancia, token de la demo, puerto, repo, horas máximas |
| `red.tf` | la VPC, el Internet Gateway, la subred pública (en una zona que tenga el tipo de instancia) y su ruta |
| `seguridad.tf` | el security group (solo el puerto de la demo) y el rol de IAM de la máquina |
| `datos.tf` | el bucket efímero y la subida del dataset |
| `computo.tf` | la EC2 (Ubuntu 24.04, IMDSv2, disco cifrado) |
| `arranque.sh.tftpl` | el script que corre la máquina al arrancar (plantilla de Terraform) |
| `outputs.tf` | la IP, la URL del presentador y los comandos de SSM |

## Una vez: requisitos en la notebook

- **El dataset** en `data/` (en la raíz del repo).
- **El plugin de SSM** de la AWS CLI (para entrar a la máquina y para el
  túnel de Grafana):

  ```sh
  curl -fsSLo /tmp/ssm.deb https://s3.amazonaws.com/session-manager-downloads/plugin/latest/ubuntu_64bit/session-manager-plugin.deb
  sudo dpkg -i /tmp/ssm.deb
  ```

- **`terraform.tfvars`** (privado, no se versiona), copiando
  `terraform.tfvars.example`. Un token nuevo por presentación:
  `openssl rand -hex 8`.

## Una sesión

```sh
cd infra/entornos/fase1
terraform init           # la primera vez
terraform apply          # ~2 min de Terraform + subir el dataset (~500MB)
```

La máquina tarda **~10 minutos más** en estar lista (instala Docker,
construye las imágenes, levanta todo). Para seguir el arranque:

```sh
eval "$(terraform output -raw ver_arranque)"   # termina cuando dice LISTO (Ctrl+C para salir)
```

Cuando dice **LISTO**:

```sh
terraform output -raw url_presentador    # abrirla en el proyector: el QR
eval "$(terraform output -raw tunel_grafana)"  # y Grafana en http://localhost:3000 (dejarlo abierto)
```

Para la demo (CPU en vivo, carga de fondo), una consola en la máquina:

```sh
eval "$(terraform output -raw entrar)"
sudo -i
cd /opt/drts
make demo-cpu CPUS=1      # el motor limitado a 1 núcleo
make demo-carga           # la carga de fondo (recalibrar RPS en el ensayo)
make demo-cpu CPUS=4      # más capacidad, en vivo
```

## Al terminar

```sh
terraform destroy         # borra TODO: la máquina, la red, el bucket y el dataset
```

**Red de seguridad:** si te olvidás, la máquina se apaga y se borra sola a
las `horas_maximas` (4 por defecto). La red y el bucket quedan (no cuestan
casi nada) hasta el próximo `destroy`. Si la máquina se borró sola, el
próximo `terraform apply` la crea de nuevo.

## Qué cuesta

La c7i.xlarge on-demand, ~$0,18 por hora, mientras existe. La IP pública
~$0,005 por hora. El bucket y el tráfico de la demo, centavos. **Sin NAT
Gateway ni balanceador** en esta fase. Una sesión de 3-4 horas: menos de un
dólar.

## Seguridad

- **Sin SSH ni puerto 22:** se entra por SSM, con las credenciales de AWS.
- **Solo el puerto de la demo** abierto a internet. Grafana y Prometheus
  escuchan en `localhost` de la máquina y se ven por el túnel de SSM.
- **El token de la demo** está en el QR (es lo que lo hace entrar) y en el
  `.env` de la máquina (solo root lo lee). El script de arranque no lo
  imprime en los logs.
- **IMDSv2 obligatorio**, y los contenedores no llegan a las credenciales de
  la máquina (hop limit 1).
- **El rol solo puede leer el bucket del dataset de esta sesión.**
