## Horizon Network Service

This is the Horizon Network Service project. It provides the ability for individual agents to advertise
and connect to other agents on a hosted set of network hubs.

It is explicitly designed for operational robustness via simplicity. For instance, the hub components can
run when completely cut of from the control plane (called the central teir) and will gracefully rejoin when
the control plane is restored.

It relies on S3 for long term, persistent service routing information.


### Architectural Notes

#### Activity

There are 2 separate systems that involving passing information about events that have occured and both are
named activity.

One passes data between central teir services using postgresql, by writing records to a table
and issuing a postgresql `NOTIFY` command. This is used to flood routing updates on the control plane.
Future work on the control plane may remove this system in favor of using a format messaging service, but
for now it serves it's purpose fine and reduces the dependencies that the control plane has.

The second activity system is one used between central tier services and hubs. Hubs make a long running
gRPC bidirectional stream connection to the central tier which is picked up by one of individual running
instances. This stream is used to pass activity like routing updates but also statitics the hubs hold to
the control plane.
